package mirakurun

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

var testPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}

func TestLogoAdapterReadsBoundedPNGWithoutProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/services/100002/logo" || request.Header.Get("Accept") != "image/png" {
			t.Fatalf("request=%s accept=%q", request.URL.Path, request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(testPNG)
	}))
	defer server.Close()
	adapter, err := NewLogo(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.CloseIdleConnections()
	data, err := adapter.Logo(context.Background(), logoTarget(t, "100002"))
	if err != nil || !bytes.Equal(data, testPNG) {
		t.Fatalf("len=%d err=%v", len(data), err)
	}
}

func TestLogoAdapterClassifiesHTTPAndBodyFailures(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		encoding    string
		body        []byte
		want        provider.Reason
	}{
		{name: "not found", status: http.StatusNotFound, want: provider.ReasonNotFound},
		{name: "logo unavailable", status: http.StatusServiceUnavailable, want: provider.ReasonNotFound},
		{name: "redirect", status: http.StatusFound, want: provider.ReasonRejected},
		{name: "client error", status: http.StatusBadRequest, want: provider.ReasonRejected},
		{name: "server error", status: http.StatusInternalServerError, want: provider.ReasonUnavailable},
		{name: "unexpected success", status: http.StatusCreated, want: provider.ReasonMalformed},
		{name: "wrong content type", status: http.StatusOK, contentType: "application/octet-stream", body: testPNG, want: provider.ReasonMalformed},
		{name: "content type parameter", status: http.StatusOK, contentType: "image/png; charset=binary", body: testPNG, want: provider.ReasonMalformed},
		{name: "compressed", status: http.StatusOK, contentType: "image/png", encoding: "gzip", body: testPNG, want: provider.ReasonRejected},
		{name: "empty", status: http.StatusOK, contentType: "image/png", want: provider.ReasonMalformed},
		{name: "invalid signature", status: http.StatusOK, contentType: "image/png", body: []byte("not-png"), want: provider.ReasonMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var followed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/followed" {
					followed.Add(1)
				}
				if test.status == http.StatusFound {
					writer.Header().Set("Location", "/followed")
				}
				if test.contentType != "" {
					writer.Header().Set("Content-Type", test.contentType)
				}
				if test.encoding != "" {
					writer.Header().Set("Content-Encoding", test.encoding)
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write(test.body)
			}))
			defer server.Close()
			adapter, err := NewLogo(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.CloseIdleConnections()
			_, err = adapter.Logo(context.Background(), logoTarget(t, "100002"))
			if !provider.IsReason(err, test.want) || followed.Load() != 0 {
				t.Fatalf("err=%v followed=%d", err, followed.Load())
			}
		})
	}
}

func TestLogoAdapterEnforcesDeclaredAndChunkedBodyLimit(t *testing.T) {
	for _, test := range []struct {
		name    string
		length  int
		chunked bool
		wantErr bool
	}{
		{name: "exact", length: int(logoBodyLimit)},
		{name: "declared over", length: int(logoBodyLimit + 1), wantErr: true},
		{name: "chunked over", length: int(logoBodyLimit + 1), chunked: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := make([]byte, test.length)
			copy(body, testPNG[:8])
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "image/png")
				if test.chunked {
					flusher := writer.(http.Flusher)
					_, _ = writer.Write(body[:8])
					flusher.Flush()
					_, _ = writer.Write(body[8:])
					return
				}
				writer.Header().Set("Content-Length", intText(test.length))
				_, _ = writer.Write(body)
			}))
			defer server.Close()
			adapter, _ := NewLogo(server.URL)
			defer adapter.CloseIdleConnections()
			data, err := adapter.Logo(context.Background(), logoTarget(t, "100002"))
			if test.wantErr {
				if !provider.IsReason(err, provider.ReasonOverLimit) {
					t.Fatalf("len=%d err=%v", len(data), err)
				}
			} else if err != nil || len(data) != test.length {
				t.Fatalf("len=%d err=%v", len(data), err)
			}
		})
	}
}

func TestLogoAdapterHandlesTruncationStallAndCancel(t *testing.T) {
	t.Run("truncated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "image/png")
			writer.Header().Set("Content-Length", "64")
			_, _ = writer.Write(testPNG)
		}))
		defer server.Close()
		adapter, _ := NewLogo(server.URL)
		defer adapter.CloseIdleConnections()
		_, err := adapter.Logo(context.Background(), logoTarget(t, "100002"))
		if !provider.IsReason(err, provider.ReasonEarlyEOF) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("stall", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "image/png")
			_, _ = writer.Write(testPNG[:8])
			writer.(http.Flusher).Flush()
			<-time.After(200 * time.Millisecond)
		}))
		defer server.Close()
		adapter, _ := newLogoAdapter(server.URL, 30*time.Millisecond, logoBodyLimit, 1)
		defer adapter.CloseIdleConnections()
		_, err := adapter.Logo(context.Background(), logoTarget(t, "100002"))
		if !provider.IsReason(err, provider.ReasonTimeout) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		adapter, _ := NewLogo("http://127.0.0.1:1")
		defer adapter.CloseIdleConnections()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := adapter.Logo(ctx, logoTarget(t, "100002"))
		if !provider.IsReason(err, provider.ReasonCancelled) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestLogoAdapterLimitsConcurrencyAndReleasesSlots(t *testing.T) {
	started := make(chan struct{}, maximumLogoTransfers)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		writer.Header().Set("Content-Type", "image/png")
		_, _ = writer.Write(testPNG)
	}))
	defer server.Close()
	adapter, _ := newLogoAdapter(server.URL, time.Second, logoBodyLimit, maximumLogoTransfers)
	defer adapter.CloseIdleConnections()
	results := make(chan error, maximumLogoTransfers)
	target := logoTarget(t, "100002")
	for range maximumLogoTransfers {
		go func() {
			_, err := adapter.Logo(context.Background(), target)
			results <- err
		}()
	}
	for range maximumLogoTransfers {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("logo request did not start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := adapter.Logo(ctx, logoTarget(t, "100002"))
	if !provider.IsReason(err, provider.ReasonTimeout) {
		t.Fatalf("fifth err=%v", err)
	}
	close(release)
	for range maximumLogoTransfers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogoAdapterClosesBodiesAndRejectsInvalidConstruction(t *testing.T) {
	adapter, err := newLogoAdapter("http://127.0.0.1:4510", time.Second, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	body := &trackedBody{Reader: bytes.NewReader(testPNG)}
	adapter.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"image/png"}}, Body: body, ContentLength: int64(len(testPNG))}, nil
	})}
	if _, err := adapter.Logo(context.Background(), logoTarget(t, "100002")); err != nil || !body.closed.Load() {
		t.Fatalf("closed=%v err=%v", body.closed.Load(), err)
	}
	if _, err := adapter.Logo(context.Background(), provider.TuningTarget{Opaque: "001"}); !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("target err=%v", err)
	}
	if _, err := (*LogoAdapter)(nil).Logo(context.Background(), logoTarget(t, "1")); !provider.IsReason(err, provider.ReasonInternal) {
		t.Fatalf("nil err=%v", err)
	}
	for _, create := range []func() (*LogoAdapter, error){
		func() (*LogoAdapter, error) { return newLogoAdapter("invalid", time.Second, 64, 1) },
		func() (*LogoAdapter, error) { return newLogoAdapter("http://127.0.0.1:4510", 0, 64, 1) },
		func() (*LogoAdapter, error) {
			return newLogoAdapter("http://127.0.0.1:4510", time.Second, logoBodyLimit+1, 1)
		},
		func() (*LogoAdapter, error) {
			return newLogoAdapter("http://127.0.0.1:4510", time.Second, 64, maximumLogoTransfers+1)
		},
	} {
		if _, err := create(); err == nil {
			t.Fatal("invalid adapter was accepted")
		}
	}

	before := runtime.NumGoroutine()
	adapter.CloseIdleConnections()
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
}

func logoTarget(t *testing.T, value string) provider.TuningTarget {
	t.Helper()
	target, err := provider.NewTuningTarget(value)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func intText(value int) string {
	return strconv.Itoa(value)
}

type trackedBody struct {
	*bytes.Reader
	closed atomic.Bool
}

func (body *trackedBody) Close() error { body.closed.Store(true); return nil }

var _ io.ReadCloser = (*trackedBody)(nil)
