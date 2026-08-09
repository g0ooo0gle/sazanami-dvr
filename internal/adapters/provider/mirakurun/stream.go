package mirakurun

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

type streamLimits struct {
	connectHeader time.Duration
	readIdle      time.Duration
}

var productionStreamLimits = streamLimits{connectHeader: 10 * time.Second, readIdle: 10 * time.Second}

const maximumConcurrentStreams = 8

// StreamAdapterはMirakurun互換のservice streamだけを開く専用HTTP clientを所有する。
type StreamAdapter struct {
	base   url.URL
	client *http.Client
	limits streamLimits

	mu                sync.Mutex
	active            int
	maximumConcurrent int
}

// NewStreamは安全設定を固定したstream adapterを作る。生成時にはnetworkへ接続しない。
func NewStream(baseURL string) (*StreamAdapter, error) {
	return newStreamAdapter(baseURL, productionStreamLimits)
}

// NewStreamWithLimitは明示上限まで独立した録画streamを開けるadapterを作る。
func NewStreamWithLimit(baseURL string, maximumConcurrent int) (*StreamAdapter, error) {
	return newStreamAdapterWithLimit(baseURL, productionStreamLimits, maximumConcurrent)
}

func newStreamAdapter(baseURL string, limits streamLimits) (*StreamAdapter, error) {
	return newStreamAdapterWithLimit(baseURL, limits, 1)
}

func newStreamAdapterWithLimit(baseURL string, limits streamLimits, maximumConcurrent int) (*StreamAdapter, error) {
	base, _, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if limits.connectHeader <= 0 || limits.readIdle <= 0 || maximumConcurrent < 1 || maximumConcurrent > maximumConcurrentStreams {
		return nil, provider.NewFailure(provider.ReasonInternal, "invalid-stream-http-limits")
	}
	dialer := &net.Dialer{Timeout: limits.connectHeader, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: false,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns: maximumConcurrent, MaxIdleConnsPerHost: maximumConcurrent, MaxConnsPerHost: maximumConcurrent,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: limits.connectHeader,
		ResponseHeaderTimeout: limits.connectHeader, ExpectContinueTimeout: time.Second,
		MaxResponseHeaderBytes: headerLimit, DisableCompression: true,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &StreamAdapter{base: base, client: client, limits: limits, maximumConcurrent: maximumConcurrent}, nil
}

// OpenStreamは録画またはライブ視聴の指定serviceをpriority 0で一度だけ開く。
func (adapter *StreamAdapter) OpenStream(ctx context.Context, request providerstream.Request) (providerstream.Lease, error) {
	if adapter == nil || adapter.client == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-stream-adapter")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if request.Usage != providerstream.UsageRecording && request.Usage != providerstream.UsageLive ||
		request.PriorityPolicy != "0" || !request.RequireDescrambled ||
		request.CorrelationID == "" || len(request.CorrelationID) > provider.MaxDiagnosticBytes ||
		!canonicalStreamServiceID(request.Target.Opaque) {
		return nil, provider.NewFailure(provider.ReasonRejected, "stream-request-out-of-profile")
	}
	adapter.mu.Lock()
	if adapter.active >= adapter.maximumConcurrent {
		adapter.mu.Unlock()
		return nil, provider.NewFailure(provider.ReasonRejected, "stream-request-already-active")
	}
	adapter.active++
	adapter.mu.Unlock()
	release := func() {
		adapter.mu.Lock()
		if adapter.active > 0 {
			adapter.active--
		}
		adapter.mu.Unlock()
	}

	requestContext, cancel := context.WithCancel(ctx)
	endpoint := adapter.base
	endpoint.Path = strings.TrimRight(adapter.base.Path, "/") + "/api/services/" + request.Target.Opaque + "/stream"
	endpoint.RawQuery = "decode=1"
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		cancel()
		release()
		return nil, provider.NewFailure(provider.ReasonInternal, "stream-request-build-failed")
	}
	httpRequest.Header.Set("Accept", "video/MP2T")
	httpRequest.Header.Set("X-Mirakurun-Priority", "0")
	var connectionMu sync.Mutex
	var connection net.Conn
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		connectionMu.Lock()
		connection = info.Conn
		connectionMu.Unlock()
	}}
	httpRequest = httpRequest.WithContext(httptrace.WithClientTrace(httpRequest.Context(), trace))
	response, err := adapter.client.Do(httpRequest)
	if err != nil {
		failure := transportFailure(requestContext, err)
		cancel()
		release()
		return nil, failure
	}
	if response.Body == nil {
		cancel()
		release()
		return nil, provider.NewFailure(provider.ReasonMalformed, "missing-stream-body")
	}
	fail := func(err error) (providerstream.Lease, error) {
		_ = response.Body.Close()
		cancel()
		release()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return fail(statusFailure(response.StatusCode))
	}
	if response.Header.Get("Content-Encoding") != "" && !strings.EqualFold(response.Header.Get("Content-Encoding"), "identity") {
		return fail(provider.NewFailure(provider.ReasonRejected, "stream-content-encoding-not-accepted"))
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "video/MP2T") {
		return fail(provider.NewFailure(provider.ReasonMalformed, "stream-content-type-not-mpeg-ts"))
	}
	connectionMu.Lock()
	streamConnection := connection
	connectionMu.Unlock()
	if streamConnection == nil {
		return fail(provider.NewFailure(provider.ReasonInternal, "stream-connection-unavailable"))
	}
	return &streamLease{
		body: response.Body, connection: streamConnection, cancel: cancel, release: release,
		idle: adapter.limits.readIdle, terminal: providerstream.Terminal{Reason: providerstream.TerminalActive},
	}, nil
}

// CloseIdleConnectionsは録画process終了時に再利用待ちのHTTP接続を閉じる。
func (adapter *StreamAdapter) CloseIdleConnections() {
	if adapter != nil && adapter.client != nil {
		adapter.client.CloseIdleConnections()
	}
}

func canonicalStreamServiceID(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 63)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

type streamLease struct {
	mu         sync.Mutex
	body       io.ReadCloser
	connection net.Conn
	cancel     context.CancelFunc
	release    func()
	idle       time.Duration
	closed     bool
	cancelled  bool
	terminal   providerstream.Terminal
}

// Readは最大192,512 bytesを読み、10秒進行がなければstreamを閉じる。
func (lease *streamLease) Read(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
	if len(destination) == 0 || len(destination) > provider.MaxStreamChunk {
		return 0, providerstream.Terminal{}, provider.NewFailure(provider.ReasonOverLimit, "stream-read-buffer")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		_ = lease.Cancel()
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled}, err
	}
	lease.mu.Lock()
	if lease.closed {
		terminal := lease.terminal
		lease.mu.Unlock()
		return 0, terminal, provider.NewFailure(provider.ReasonRejected, "stream-lease-closed")
	}
	if lease.terminal.Done {
		terminal := lease.terminal
		lease.mu.Unlock()
		return 0, terminal, nil
	}
	body, connection, idle := lease.body, lease.connection, lease.idle
	lease.mu.Unlock()
	if err := connection.SetReadDeadline(time.Now().Add(idle)); err != nil {
		lease.finish(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
		return 0, lease.currentTerminal(), provider.NewFailure(provider.ReasonUnavailable, "stream-read-deadline-failed")
	}
	read, err := body.Read(destination)
	if read > len(destination) || read < 0 {
		lease.finish(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
		return 0, lease.currentTerminal(), provider.NewFailure(provider.ReasonInternal, "invalid-stream-read-count")
	}
	if err == nil && read > 0 {
		return read, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}
	if err == nil {
		lease.finish(providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer})
		return 0, lease.currentTerminal(), provider.NewFailure(provider.ReasonUnavailable, "zero-progress-stream-read")
	}
	terminal := providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer}
	failure := provider.NewFailure(provider.ReasonUnavailable, "stream-read-failed")
	if errors.Is(err, io.EOF) {
		terminal.Reason = providerstream.TerminalEarlyEOF
		failure = provider.NewFailure(provider.ReasonEarlyEOF, "stream-ended")
	} else if provider.ContextFailure(ctx) != nil || lease.wasCancelled() {
		terminal.Reason = providerstream.TerminalCancelled
		failure = provider.NewFailure(provider.ReasonCancelled, "stream-cancelled")
	} else {
		var netError net.Error
		if errors.As(err, &netError) && netError.Timeout() {
			terminal.Reason = providerstream.TerminalTimeout
			failure = provider.NewFailure(provider.ReasonTimeout, "stream-read-timeout")
		}
	}
	lease.finish(terminal)
	return read, terminal, failure
}

// Cancelは進行中のHTTP readを中断し、何度呼ばれても同じ終了状態を保つ。
func (lease *streamLease) Cancel() error {
	lease.mu.Lock()
	if lease.cancelled {
		lease.mu.Unlock()
		return nil
	}
	lease.cancelled = true
	cancel := lease.cancel
	lease.mu.Unlock()
	cancel()
	lease.finish(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled})
	return nil
}

// CloseはHTTP bodyと接続期限を解放し、何度呼ばれても安全に終了する。
func (lease *streamLease) Close() error {
	lease.finish(providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled})
	return nil
}

func (lease *streamLease) finish(terminal providerstream.Terminal) {
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return
	}
	lease.closed = true
	lease.terminal = terminal
	body, connection, cancel, release := lease.body, lease.connection, lease.cancel, lease.release
	lease.mu.Unlock()
	cancel()
	_ = body.Close()
	_ = connection.SetReadDeadline(time.Time{})
	release()
}

func (lease *streamLease) currentTerminal() providerstream.Terminal {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.terminal
}

func (lease *streamLease) wasCancelled() bool {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.cancelled
}

var _ providerstream.Provider = (*StreamAdapter)(nil)
