package mirakurun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

const (
	logoBodyLimit        = int64(2 * 1024 * 1024)
	logoHeaderLimit      = int64(64 * 1024)
	logoTimeout          = 5 * time.Second
	maximumLogoTransfers = 4
)

// LogoAdapterは検証済みのMirakurunサービスIDに対応するPNGだけを上限付きで取得する。
// 生成時には接続せず、取得した画像を永続化しない。
type LogoAdapter struct {
	base      url.URL
	client    *http.Client
	timeout   time.Duration
	bodyLimit int64
	gate      chan struct{}
}

// NewLogoはproxyやredirectを使わない局ロゴ専用adapterを作る。
func NewLogo(baseURL string) (*LogoAdapter, error) {
	return newLogoAdapter(baseURL, logoTimeout, logoBodyLimit, maximumLogoTransfers)
}

func newLogoAdapter(baseURL string, timeout time.Duration, bodyLimit int64, maximum int) (*LogoAdapter, error) {
	base, _, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 || bodyLimit < 1 || bodyLimit > logoBodyLimit || maximum < 1 || maximum > maximumLogoTransfers {
		return nil, provider.NewFailure(provider.ReasonInternal, "invalid-logo-http-limits")
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
		MaxIdleConns: maximum, MaxIdleConnsPerHost: maximum, MaxConnsPerHost: maximum,
		IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: timeout,
		ResponseHeaderTimeout: timeout, ExpectContinueTimeout: time.Second,
		MaxResponseHeaderBytes: logoHeaderLimit, DisableCompression: true,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &LogoAdapter{base: base, client: client, timeout: timeout, bodyLimit: bodyLimit, gate: make(chan struct{}, maximum)}, nil
}

// Logoは一つのサービスのPNGを読み切り、2 MiB以下のbyte列として返す。
// 同時取得枠の待機を含む全処理を5秒以内に制限する。
func (adapter *LogoAdapter) Logo(ctx context.Context, target provider.TuningTarget) ([]byte, error) {
	if adapter == nil || adapter.client == nil || adapter.gate == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-logo-adapter")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	if !canonicalStreamServiceID(target.Opaque) {
		return nil, provider.NewFailure(provider.ReasonRejected, "logo-target-out-of-profile")
	}
	operationContext, cancel := context.WithTimeout(ctx, adapter.timeout)
	defer cancel()
	select {
	case adapter.gate <- struct{}{}:
		defer func() { <-adapter.gate }()
	case <-operationContext.Done():
		return nil, provider.ContextFailure(operationContext)
	}

	endpoint := adapter.base
	endpoint.Path = strings.TrimRight(adapter.base.Path, "/") + "/api/services/" + target.Opaque + "/logo"
	request, err := http.NewRequestWithContext(operationContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "logo-request-build-failed")
	}
	request.Header.Set("Accept", "image/png")
	response, err := adapter.client.Do(request)
	if err != nil {
		return nil, transportFailure(operationContext, err)
	}
	if response.Body == nil {
		return nil, provider.NewFailure(provider.ReasonMalformed, "missing-logo-body")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusServiceUnavailable {
		return nil, provider.NewFailure(provider.ReasonNotFound, "logo-not-found")
	}
	if response.StatusCode != http.StatusOK {
		return nil, statusFailure(response.StatusCode)
	}
	if response.ContentLength > adapter.bodyLimit {
		return nil, provider.NewFailure(provider.ReasonOverLimit, "logo-content-length-over-limit")
	}
	if response.Header.Get("Content-Encoding") != "" && !strings.EqualFold(response.Header.Get("Content-Encoding"), "identity") {
		return nil, provider.NewFailure(provider.ReasonRejected, "logo-content-encoding-not-accepted")
	}
	if !strings.EqualFold(strings.TrimSpace(response.Header.Get("Content-Type")), "image/png") {
		return nil, provider.NewFailure(provider.ReasonMalformed, "logo-content-type-not-png")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, adapter.bodyLimit+1))
	if err != nil {
		if failure := provider.ContextFailure(operationContext); failure != nil {
			return nil, failure
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, provider.NewFailure(provider.ReasonEarlyEOF, "logo-body-truncated")
		}
		return nil, provider.NewFailure(provider.ReasonUnavailable, "logo-body-read-failed")
	}
	if int64(len(data)) > adapter.bodyLimit {
		return nil, provider.NewFailure(provider.ReasonOverLimit, "logo-body-over-limit")
	}
	if len(data) == 0 {
		return nil, provider.NewFailure(provider.ReasonMalformed, "logo-body-empty")
	}
	if !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return nil, provider.NewFailure(provider.ReasonMalformed, "logo-signature-invalid")
	}
	return data, nil
}

// CloseIdleConnectionsは録画process終了時に再利用待ちのロゴ接続を閉じる。
func (adapter *LogoAdapter) CloseIdleConnections() {
	if adapter != nil && adapter.client != nil {
		adapter.client.CloseIdleConnections()
	}
}
