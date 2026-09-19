package mirakurun

import (
	"bytes"
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
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

type streamLimits struct {
	connectHeader time.Duration
	readIdle      time.Duration
}

var productionStreamLimits = streamLimits{connectHeader: 10 * time.Second, readIdle: 10 * time.Second}

const (
	probeDeadline            = 15 * time.Second
	probeMaxBytes            = 1 << 20
	probeReadBytes           = 32 * 1024
	sdtProbeMaxBytes         = 64 * 1024 * 1024
	sdtProbeMaxSections      = 256
	sdtProbeMaxServices      = 4_096
	sdtProbeSectionBytes     = 256 * 1024
	sdtProbeManagementBytes  = 64 * 1024
	sdtProbeServiceBitsetLen = 1 << 13
)

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
	if limits.connectHeader <= 0 || limits.readIdle <= 0 || maximumConcurrent < 1 {
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
	response, err := adapter.openServiceStream(ctx, request.Target.Opaque, "decode=1", 0)
	if err != nil {
		return nil, err
	}
	return &streamLease{
		body: response.body, connection: response.connection, cancel: response.cancel, release: response.release,
		idle: adapter.limits.readIdle, terminal: providerstream.Terminal{Reason: providerstream.TerminalActive},
	}, nil
}

// ProbeTransportStreamIDはservice streamのPID 0からPATを一件だけ検証し、TSIDを返す。
// Probeは通常の録画・ライブstreamと分離したdecode=0の短時間操作である。
func (adapter *StreamAdapter) ProbeTransportStreamID(ctx context.Context, providerLocator string, serviceID uint16) (uint16, error) {
	response, err := adapter.openServiceStream(ctx, providerLocator, "decode=0", probeDeadline)
	if err != nil {
		return 0, err
	}
	defer response.close()
	if response.contentLength > probeMaxBytes {
		return 0, provider.NewFailure(provider.ReasonOverLimit, "probe-content-length-over-limit")
	}

	var packetizer mpegts.Packetizer
	var collector mpegts.PSICollector
	var found *mpegts.PAT
	buffer := make([]byte, probeReadBytes)
	total := 0
	for total < probeMaxBytes {
		readBuffer := buffer
		if remaining := probeMaxBytes - total; len(readBuffer) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		if err := response.connection.SetReadDeadline(time.Now().Add(response.idle)); err != nil {
			return 0, provider.NewFailure(provider.ReasonUnavailable, "probe-read-deadline-failed")
		}
		read, readErr := response.body.Read(readBuffer)
		if read < 0 || read > len(readBuffer) {
			return 0, provider.NewFailure(provider.ReasonInternal, "probe-invalid-read-count")
		}
		total += read
		if read > 0 {
			feedErr := packetizer.Feed(readBuffer[:read], func(packet []byte) error {
				if mpegts.PID(packet) != 0 || found != nil {
					return nil
				}
				sections, sectionErr := collector.Feed(packet)
				if sectionErr != nil {
					return sectionErr
				}
				for _, section := range sections {
					pat, parseErr := mpegts.ParsePAT(section)
					if parseErr != nil {
						return parseErr
					}
					if pat.ProgramNumber != serviceID {
						return provider.NewFailure(provider.ReasonMalformed, "probe-service-id-mismatch")
					}
					value := pat
					found = &value
					break
				}
				return nil
			})
			if feedErr != nil {
				return 0, probeParserFailure(feedErr)
			}
			if found != nil {
				return found.TransportStreamID, nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return 0, provider.NewFailure(provider.ReasonEarlyEOF, "probe-pat-not-found")
			}
			return 0, classifyProbeReadFailure(response.ctx, readErr)
		}
		if read == 0 {
			return 0, provider.NewFailure(provider.ReasonUnavailable, "probe-zero-progress")
		}
	}
	return 0, provider.NewFailure(provider.ReasonOverLimit, "probe-byte-limit")
}

// ProbeTransportStreamIDWithSDTFallbackはservice PAT確認を先に一回だけ行い、
// 許可された一時的失敗に限って同じproviderのchannel SDT actualを確認する。
func (adapter *StreamAdapter) ProbeTransportStreamIDWithSDTFallback(ctx context.Context, service BootstrapService) (uint16, error) {
	transportStreamID, err := adapter.ProbeTransportStreamID(ctx, service.Locator, service.ServiceID)
	if err == nil || !allowsPATSDTFallback(ctx, err) || !validBootstrapChannel(service.Channel) {
		return transportStreamID, err
	}
	return adapter.ProbeTransportStreamIDFromChannel(ctx, *service.Channel, service.NetworkID, service.ServiceID)
}

// ProbeTransportStreamIDFromChannelはchannel streamのSDT actualから対象serviceのTSIDを返す。
func (adapter *StreamAdapter) ProbeTransportStreamIDFromChannel(ctx context.Context, channel BootstrapChannel,
	networkID, serviceID uint16,
) (uint16, error) {
	response, err := adapter.openChannelStream(ctx, channel, "decode=0", probeDeadline)
	if err != nil {
		return 0, err
	}
	defer response.close()
	if response.contentLength > sdtProbeMaxBytes {
		return 0, provider.NewFailure(provider.ReasonOverLimit, "probe-sdt-content-length-over-limit")
	}

	var packetizer mpegts.Packetizer
	var collector mpegts.PSICollector
	generation := sdtGeneration{}
	completed := false
	buffer := make([]byte, probeReadBytes)
	total := 0
	for total < sdtProbeMaxBytes {
		readBuffer := buffer
		if remaining := sdtProbeMaxBytes - total; len(readBuffer) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		if err := response.connection.SetReadDeadline(time.Now().Add(response.idle)); err != nil {
			return 0, provider.NewFailure(provider.ReasonUnavailable, "probe-sdt-read-deadline-failed")
		}
		read, readErr := response.body.Read(readBuffer)
		if read < 0 || read > len(readBuffer) {
			return 0, provider.NewFailure(provider.ReasonInternal, "probe-sdt-invalid-read-count")
		}
		total += read
		if read > 0 {
			feedErr := packetizer.Feed(readBuffer[:read], func(packet []byte) error {
				if completed {
					return nil
				}
				if mpegts.PID(packet) != 0x0011 {
					return nil
				}
				sections, sectionErr := collector.Feed(packet)
				if sectionErr != nil {
					return sectionErr
				}
				for _, section := range sections {
					if len(section) == 0 || section[0] != 0x42 {
						continue
					}
					sdt, parseErr := mpegts.ParseSDT(section)
					if parseErr != nil {
						return parseErr
					}
					if _, complete, acceptErr := generation.accept(sdt, section, networkID, serviceID); acceptErr != nil {
						return acceptErr
					} else if complete {
						completed = true
						return nil
					}
				}
				return nil
			})
			if feedErr != nil {
				return 0, sdtParserFailure(feedErr)
			}
			if transportStreamID, complete, resultErr := generation.result(serviceID); complete {
				if resultErr != nil {
					return 0, resultErr
				}
				return transportStreamID, nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return 0, provider.NewFailure(provider.ReasonEarlyEOF, "probe-sdt-not-found")
			}
			return 0, classifyProbeReadFailure(response.ctx, readErr)
		}
		if read == 0 {
			return 0, provider.NewFailure(provider.ReasonUnavailable, "probe-sdt-zero-progress")
		}
	}
	return 0, provider.NewFailure(provider.ReasonOverLimit, "probe-sdt-byte-limit")
}

func allowsPATSDTFallback(ctx context.Context, err error) bool {
	if provider.ContextFailure(ctx) != nil {
		return false
	}
	var failure *provider.Failure
	if !errors.As(err, &failure) {
		return false
	}
	switch {
	case failure.Reason == provider.ReasonTimeout && failure.Diagnostic == "http-timeout":
		return true
	case failure.Reason == provider.ReasonTimeout && failure.Diagnostic == "probe-read-timeout":
		return true
	case failure.Reason == provider.ReasonTimeout && failure.Diagnostic == "context-deadline":
		return true
	case failure.Reason == provider.ReasonEarlyEOF && failure.Diagnostic == "probe-pat-not-found":
		return true
	case failure.Reason == provider.ReasonOverLimit && failure.Diagnostic == "probe-byte-limit":
		return true
	default:
		return false
	}
}

type sdtGeneration struct {
	initialized       bool
	transportStreamID uint16
	originalNetworkID uint16
	version           byte
	lastSection       byte
	sections          [sdtProbeMaxSections][]byte
	sectionCount      int
	sectionBytes      int
	serviceBits       [sdtProbeServiceBitsetLen]byte
	serviceCount      int
}

func (generation *sdtGeneration) accept(table mpegts.SDT, section []byte, networkID, serviceID uint16) (uint16, bool, error) {
	if !table.CurrentNext {
		return 0, false, nil
	}
	if !generation.initialized {
		if table.OriginalNetworkID != networkID {
			return 0, false, provider.NewFailure(provider.ReasonMalformed, "probe-sdt-network-id-mismatch")
		}
		generation.initialized = true
		generation.transportStreamID = table.TransportStreamID
		generation.originalNetworkID = table.OriginalNetworkID
		generation.version = table.Version
		generation.lastSection = table.LastSectionNumber
	} else if table.OriginalNetworkID != generation.originalNetworkID || table.TransportStreamID != generation.transportStreamID ||
		table.Version != generation.version || table.LastSectionNumber != generation.lastSection {
		return 0, false, provider.NewFailure(provider.ReasonMalformed, "probe-sdt-generation-changed")
	}

	if existing := generation.sections[table.SectionNumber]; existing != nil {
		if !bytes.Equal(existing, section) {
			return 0, false, provider.NewFailure(provider.ReasonMalformed, "probe-sdt-section-changed")
		}
		return generation.result(serviceID)
	}
	if generation.sectionCount >= sdtProbeMaxSections || generation.sectionBytes+len(section) > sdtProbeSectionBytes {
		return 0, false, provider.NewFailure(provider.ReasonOverLimit, "probe-sdt-section-limit")
	}
	newServices := 0
	for _, candidate := range table.ServiceIDs {
		index, mask := candidate/8, byte(1<<uint(candidate%8))
		if generation.serviceBits[index]&mask != 0 {
			return 0, false, provider.NewFailure(provider.ReasonMalformed, "probe-sdt-service-duplicate")
		}
		newServices++
	}
	if generation.serviceCount+newServices > sdtProbeMaxServices {
		return 0, false, provider.NewFailure(provider.ReasonOverLimit, "probe-sdt-service-limit")
	}
	if generation.managementBytes(generation.sectionCount+1, generation.serviceCount+newServices) > sdtProbeManagementBytes {
		return 0, false, provider.NewFailure(provider.ReasonOverLimit, "probe-sdt-management-limit")
	}
	for _, candidate := range table.ServiceIDs {
		index, mask := candidate/8, byte(1<<uint(candidate%8))
		generation.serviceBits[index] |= mask
		generation.serviceCount++
	}
	generation.sections[table.SectionNumber] = append([]byte(nil), section...)
	generation.sectionCount++
	generation.sectionBytes += len(section)
	return generation.result(serviceID)
}

func (generation *sdtGeneration) result(serviceID uint16) (uint16, bool, error) {
	if !generation.initialized || generation.sectionCount != int(generation.lastSection)+1 {
		return 0, false, nil
	}
	for sectionNumber := 0; sectionNumber <= int(generation.lastSection); sectionNumber++ {
		if generation.sections[sectionNumber] == nil {
			return 0, false, nil
		}
	}
	if !generation.hasService(serviceID) {
		return 0, true, provider.NewFailure(provider.ReasonMalformed, "probe-sdt-service-not-found")
	}
	return generation.transportStreamID, true, nil
}

func (generation *sdtGeneration) hasService(serviceID uint16) bool {
	return generation.serviceBits[serviceID/8]&(1<<uint(serviceID%8)) != 0
}

func (generation *sdtGeneration) managementBytes(sectionCount, serviceCount int) int {
	return sdtProbeServiceBitsetLen + sectionCount*16 + serviceCount*2
}

func sdtParserFailure(err error) error {
	var providerFailure *provider.Failure
	if errors.As(err, &providerFailure) {
		return providerFailure
	}
	if errors.Is(err, mpegts.ErrSync) || errors.Is(err, mpegts.ErrPacket) || errors.Is(err, mpegts.ErrPSI) {
		return provider.NewFailure(provider.ReasonMalformed, "probe-sdt-invalid")
	}
	return provider.NewFailure(provider.ReasonInternal, "probe-sdt-parser-failed")
}

type streamResponse struct {
	ctx           context.Context
	body          io.ReadCloser
	connection    net.Conn
	cancel        context.CancelFunc
	release       func()
	idle          time.Duration
	contentLength int64
}

func (response *streamResponse) close() {
	if response == nil {
		return
	}
	response.cancel()
	_ = response.body.Close()
	_ = response.connection.SetReadDeadline(time.Time{})
	response.release()
}

func (adapter *StreamAdapter) openServiceStream(ctx context.Context, providerLocator, query string, deadline time.Duration) (*streamResponse, error) {
	if adapter == nil || adapter.client == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-stream-adapter")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if !canonicalStreamServiceID(providerLocator) {
		return nil, provider.NewFailure(provider.ReasonRejected, "stream-request-out-of-profile")
	}
	return adapter.openStream(ctx, "/api/services/"+providerLocator+"/stream", query, deadline)
}

func (adapter *StreamAdapter) openChannelStream(ctx context.Context, channel BootstrapChannel, query string,
	deadline time.Duration,
) (*streamResponse, error) {
	if adapter == nil || adapter.client == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-stream-adapter")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if !validBootstrapChannel(&channel) {
		return nil, provider.NewFailure(provider.ReasonRejected, "stream-channel-out-of-profile")
	}
	return adapter.openStream(ctx, "/api/channels/"+channel.Type+"/"+channel.Channel+"/stream", query, deadline)
}

func (adapter *StreamAdapter) openStream(ctx context.Context, suffix, query string, deadline time.Duration) (*streamResponse, error) {
	if adapter == nil || adapter.client == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-stream-adapter")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
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

	requestContext := ctx
	var cancel context.CancelFunc
	if deadline > 0 {
		requestContext, cancel = context.WithTimeout(ctx, deadline)
	} else {
		requestContext, cancel = context.WithCancel(ctx)
	}
	endpoint := adapter.base
	endpoint.Path = strings.TrimRight(adapter.base.Path, "/") + suffix
	endpoint.RawQuery = query
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
	if response == nil || response.Body == nil {
		cancel()
		release()
		return nil, provider.NewFailure(provider.ReasonMalformed, "missing-stream-body")
	}
	fail := func(err error) (*streamResponse, error) {
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
	return &streamResponse{
		ctx: requestContext, body: response.Body, connection: streamConnection, cancel: cancel, release: release,
		idle: adapter.limits.readIdle, contentLength: response.ContentLength,
	}, nil
}

func probeParserFailure(err error) error {
	var providerFailure *provider.Failure
	if errors.As(err, &providerFailure) {
		return providerFailure
	}
	if errors.Is(err, mpegts.ErrSync) || errors.Is(err, mpegts.ErrPacket) || errors.Is(err, mpegts.ErrPSI) || errors.Is(err, mpegts.ErrSingleProgram) {
		return provider.NewFailure(provider.ReasonMalformed, "probe-pat-invalid")
	}
	return provider.NewFailure(provider.ReasonInternal, "probe-parser-failed")
}

func classifyProbeReadFailure(ctx context.Context, err error) error {
	if failure := provider.ContextFailure(ctx); failure != nil {
		return failure
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return provider.NewFailure(provider.ReasonTimeout, "probe-read-timeout")
	}
	return provider.NewFailure(provider.ReasonUnavailable, "probe-read-failed")
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
