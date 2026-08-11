// Package mirakurunはMirakurun互換のcatalog HTTP／JSON境界を実装する。
// Tuner、stream、TSには触れず、serviceと番組を上限付きcursorへ変換する。
package mirakurun

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

const (
	versionBodyLimit  = int64(64 * 1024)
	tunersBodyLimit   = int64(1024 * 1024)
	servicesBodyLimit = int64(16 * 1024 * 1024)
	programsBodyLimit = int64(256 * 1024 * 1024)
	headerLimit       = int64(64 * 1024)
)

type operationLimits struct {
	connectHeader time.Duration
	version       time.Duration
	tuners        time.Duration
	services      time.Duration
	programs      time.Duration
}

var productionLimits = operationLimits{
	connectHeader: 5 * time.Second,
	version:       5 * time.Second,
	tuners:        5 * time.Second,
	services:      30 * time.Second,
	programs:      5 * time.Minute,
}

// Adapterは1つの正規化済みendpointと逐次HTTP clientを所有する。
// Endpoint文字列は公開せず、永続identityにはSHA-256だけを渡す。
type Adapter struct {
	base       url.URL
	client     *http.Client
	identity   [32]byte
	limits     operationLimits
	versionCap int64
	tunerCap   int64
	serviceCap int64
	programCap int64
	provenance provider.Provenance

	mu     sync.Mutex
	active bool
}

// VersionObservationはtunerを使わずに取得したboundedなruntime versionである。
type VersionObservation struct {
	Current string
	Latest  string
}

// Newは固定した安全設定でMirakurun catalog adapterを作る。生成時にはnetworkへ接続しない。
func New(baseURL string) (*Adapter, error) {
	return newAdapter(baseURL, productionLimits)
}

func newAdapter(baseURL string, limits operationLimits) (*Adapter, error) {
	base, normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if limits.connectHeader <= 0 || limits.version <= 0 || limits.tuners <= 0 || limits.services <= 0 || limits.programs <= 0 {
		return nil, provider.NewFailure(provider.ReasonInternal, "invalid-http-limits")
	}
	dialer := &net.Dialer{Timeout: limits.connectHeader, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        1,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    limits.connectHeader,
		ResponseHeaderTimeout:  limits.connectHeader,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: headerLimit,
		DisableCompression:     true,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	provenance, err := provider.NewProvenance("MIRAKURUN", "catalog-http-v1")
	if err != nil {
		return nil, err
	}
	return &Adapter{
		base: base, client: client, identity: sha256.Sum256([]byte(normalized)), limits: limits,
		versionCap: versionBodyLimit, tunerCap: tunersBodyLimit, serviceCap: servicesBodyLimit, programCap: programsBodyLimit,
		provenance: provenance,
	}, nil
}

// IdentityHashはcredentialを含まない正規化済みendpointから作ったdefensive copyを返す。
func (adapter *Adapter) IdentityHash() [32]byte {
	if adapter == nil {
		return [32]byte{}
	}
	return adapter.identity
}

// CloseIdleConnectionsはone-shot操作後に再利用待ちのHTTP接続を閉じる。
func (adapter *Adapter) CloseIdleConnections() {
	if adapter != nil && adapter.client != nil {
		adapter.client.CloseIdleConnections()
	}
}

// ObserveVersionは/api/version相当をboundedに読み、version文字列だけで互換性を判定しない。
func (adapter *Adapter) ObserveVersion(ctx context.Context) (VersionObservation, error) {
	var result VersionObservation
	operation, err := adapter.open(ctx, "/api/version", adapter.versionCap, adapter.limits.version)
	if err != nil {
		return result, err
	}
	defer operation.close()
	result, err = decodeVersion(operation.decoder)
	if err != nil {
		return VersionObservation{}, operation.failure(err)
	}
	if err := operation.finishDocument(); err != nil {
		return VersionObservation{}, operation.failure(err)
	}
	return result, nil
}

// ObserveTunerCountは/api/tunersを逐次検証し、JSON objectの件数だけを返す。
// fieldの内容から空き台数や対応放送種別を推測せず、応答全体も保持しない。
func (adapter *Adapter) ObserveTunerCount(ctx context.Context) (int, error) {
	if adapter == nil {
		return 0, provider.NewFailure(provider.ReasonInternal, "nil-adapter")
	}
	defer adapter.CloseIdleConnections()
	operation, err := adapter.open(ctx, "/api/tuners", adapter.tunerCap, adapter.limits.tuners)
	if err != nil {
		return 0, err
	}
	if err := operation.beginArray(); err != nil {
		return 0, operation.failure(err)
	}
	count := 0
	for operation.decoder.More() {
		if count == math.MaxInt {
			return 0, operation.failure(provider.NewFailure(provider.ReasonOverLimit, "tuner-count-overflow"))
		}
		if err := decodeTuner(operation.decoder); err != nil {
			return 0, operation.failure(err)
		}
		count++
	}
	if err := operation.finishArray(); err != nil {
		return 0, operation.failure(err)
	}
	if count == 0 {
		return 0, operation.failure(provider.NewFailure(provider.ReasonMalformed, "tuner-list-empty"))
	}
	if err := operation.close(); err != nil {
		return 0, err
	}
	return count, nil
}

// OpenServicesは/api/services相当を開き、最大256件ずつ返すcursorを生成する。
func (adapter *Adapter) OpenServices(ctx context.Context, request catalog.ServiceRequest) (catalog.ServiceCursor, error) {
	limit, err := validateRequest(ctx, request.CorrelationID, request.Limit)
	if err != nil {
		return nil, err
	}
	operation, err := adapter.open(ctx, "/api/services", adapter.serviceCap, adapter.limits.services)
	if err != nil {
		return nil, err
	}
	if err := operation.beginArray(); err != nil {
		operation.close()
		return nil, operation.failure(err)
	}
	return &serviceCursor{operation: operation, limit: limit, provenance: adapter.provenance}, nil
}

// OpenProgramsは/api/programs相当を開き、最大256件ずつ返すcursorを生成する。
func (adapter *Adapter) OpenPrograms(ctx context.Context, request catalog.ProgramRequest) (catalog.ProgramCursor, error) {
	limit, err := validateRequest(ctx, request.CorrelationID, request.Limit)
	if err != nil {
		return nil, err
	}
	operation, err := adapter.open(ctx, "/api/programs", adapter.programCap, adapter.limits.programs)
	if err != nil {
		return nil, err
	}
	if err := operation.beginArray(); err != nil {
		operation.close()
		return nil, operation.failure(err)
	}
	return &programCursor{operation: operation, limit: limit, provenance: adapter.provenance}, nil
}

func validateRequest(ctx context.Context, correlation string, requested int) (int, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return 0, err
	}
	if correlation == "" || len(correlation) > provider.MaxDiagnosticBytes {
		return 0, provider.NewFailure(provider.ReasonMalformed, "invalid-correlation")
	}
	return provider.EffectiveLimit(requested, provider.MaxCatalogPage)
}

func (adapter *Adapter) open(ctx context.Context, suffix string, bodyLimit int64, deadline time.Duration) (*responseOperation, error) {
	if adapter == nil || adapter.client == nil {
		return nil, provider.NewFailure(provider.ReasonInternal, "nil-adapter")
	}
	if err := provider.ContextFailure(ctx); err != nil {
		return nil, err
	}
	adapter.mu.Lock()
	if adapter.active {
		adapter.mu.Unlock()
		return nil, provider.NewFailure(provider.ReasonRejected, "request-already-active")
	}
	adapter.active = true
	adapter.mu.Unlock()
	release := func() {
		adapter.mu.Lock()
		adapter.active = false
		adapter.mu.Unlock()
	}

	requestContext, cancel := context.WithTimeout(ctx, deadline)
	endpoint := adapter.base
	endpoint.Path = strings.TrimRight(adapter.base.Path, "/") + suffix
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		cancel()
		release()
		return nil, provider.NewFailure(provider.ReasonInternal, "request-build-failed")
	}
	request.Header.Set("Accept", "application/json")
	response, err := adapter.client.Do(request)
	if err != nil {
		failure := transportFailure(requestContext, err)
		cancel()
		release()
		return nil, failure
	}
	if response.Body == nil {
		cancel()
		release()
		return nil, provider.NewFailure(provider.ReasonMalformed, "missing-response-body")
	}
	operation := newResponseOperation(requestContext, cancel, release, response.Body, bodyLimit)
	if response.StatusCode != http.StatusOK {
		operation.close()
		return nil, statusFailure(response.StatusCode)
	}
	if response.ContentLength > bodyLimit {
		operation.close()
		return nil, provider.NewFailure(provider.ReasonOverLimit, "content-length-over-limit")
	}
	if response.Header.Get("Content-Encoding") != "" && !strings.EqualFold(response.Header.Get("Content-Encoding"), "identity") {
		operation.close()
		return nil, provider.NewFailure(provider.ReasonRejected, "content-encoding-not-accepted")
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		operation.close()
		return nil, provider.NewFailure(provider.ReasonMalformed, "content-type-not-json")
	}
	return operation, nil
}

func normalizeBaseURL(raw string) (url.URL, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.Port() == "" {
		return url.URL{}, "", provider.NewFailure(provider.ReasonMalformed, "invalid-base-url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return url.URL{}, "", provider.NewFailure(provider.ReasonRejected, "base-url-scheme-not-accepted")
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.RawPath != "" || parsed.Opaque != "" {
		return url.URL{}, "", provider.NewFailure(provider.ReasonRejected, "base-url-component-not-accepted")
	}
	basePath := path.Clean("/" + strings.TrimPrefix(parsed.Path, "/"))
	if basePath == "/" {
		basePath = ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = net.JoinHostPort(strings.ToLower(parsed.Hostname()), parsed.Port())
	parsed.Path = basePath
	parsed.RawPath = ""
	normalized := parsed.Scheme + "://" + parsed.Host + parsed.Path
	return *parsed, normalized, nil
}

func statusFailure(status int) error {
	switch {
	case status >= 300 && status < 400:
		return provider.NewFailure(provider.ReasonRejected, "http-redirect")
	case status == http.StatusNotFound:
		return provider.NewFailure(provider.ReasonNotFound, "http-not-found")
	case status >= 400 && status < 500:
		return provider.NewFailure(provider.ReasonRejected, "http-client-error")
	case status >= 500:
		return provider.NewFailure(provider.ReasonUnavailable, "http-server-error")
	default:
		return provider.NewFailure(provider.ReasonMalformed, "http-status-not-accepted")
	}
}

func transportFailure(ctx context.Context, err error) error {
	if failure := provider.ContextFailure(ctx); failure != nil {
		return failure
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return provider.NewFailure(provider.ReasonTimeout, "http-timeout")
	}
	return provider.NewFailure(provider.ReasonUnavailable, "http-request-failed")
}

func numberText(value uint64) string { return fmt.Sprintf("%d", value) }

var _ catalog.CatalogProvider = (*Adapter)(nil)
