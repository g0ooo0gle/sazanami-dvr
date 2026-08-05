package mirakurun

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
)

func TestVersionAndCatalogMapping(t *testing.T) {
	server := newCatalogServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/version":
			writeJSON(writer, `{"future":{"compatible":true},"latest":"99.0.0","current":"unknown-compatible"}`)
		case "/api/services":
			writeJSON(writer, fmt.Sprintf(`[{"type":1,"unknown":{"nested":[1,true,null]},"name":"総合","serviceId":1024,"id":%d,"networkId":32736}]`, serviceProviderID(32736, 1024)))
		case "/api/programs":
			writeJSON(writer, fmt.Sprintf(`[
				{"description":"","isFree":true,"duration":null,"startAt":null,"eventId":1,"name":"未定","serviceId":1024,"networkId":32736,"id":%d,"extra":[1,2]},
				{"id":%d,"networkId":32736,"serviceId":1024,"eventId":2,"startAt":1785628800000,"duration":1800000,"isFree":false,"name":"番組","description":"説明"}
			]`, programProviderID(32736, 1024, 1), programProviderID(32736, 1024, 2)))
		default:
			http.NotFound(writer, request)
		}
	})
	defer server.Close()
	adapter := mustAdapter(t, server.URL)

	version, err := adapter.ObserveVersion(context.Background())
	if err != nil || version.Current != "unknown-compatible" || version.Latest != "99.0.0" {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	services, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "services", Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	servicePage, err := services.Next(context.Background())
	if err != nil || !servicePage.End || len(servicePage.Items) != 1 {
		t.Fatalf("page=%+v err=%v", servicePage, err)
	}
	service := servicePage.Items[0]
	if service.Locator != fmt.Sprint(serviceProviderID(32736, 1024)) || service.NetworkID != 32736 ||
		service.ServiceID != 1024 || service.DisplayName != "総合" || service.Broadcast != "1" ||
		service.Validation != provider.ValidationUnknown {
		t.Fatalf("service=%+v", service)
	}
	if err := services.Close(); err != nil {
		t.Fatal(err)
	}
	programs, err := adapter.OpenPrograms(context.Background(), catalog.ProgramRequest{CorrelationID: "programs", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	first, err := programs.Next(context.Background())
	if err != nil || first.End || len(first.Items) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if item := first.Items[0]; item.Start != nil || item.Duration != nil || item.Validation != provider.ValidationUnknown ||
		item.EventID == nil || *item.EventID != 1 || item.FreeAccess == nil || !*item.FreeAccess {
		t.Fatalf("unknown-time=%+v", item)
	}
	second, err := programs.Next(context.Background())
	if err != nil || second.End || len(second.Items) != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	final, err := programs.Next(context.Background())
	if err != nil || !final.End || len(final.Items) != 0 {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	item := second.Items[0]
	if item.Start == nil || item.Start.UnixMilli() != 1785628800000 || item.Duration == nil ||
		*item.Duration != 30*time.Minute || item.FreeAccess == nil || *item.FreeAccess || item.Title != "番組" {
		t.Fatalf("program=%+v", item)
	}
}

func TestJSONContractFailures(t *testing.T) {
	validServiceID := serviceProviderID(1, 2)
	tests := []struct {
		name   string
		body   string
		reason provider.Reason
	}{
		{name: "duplicate key", body: fmt.Sprintf(`[{"id":%d,"id":%d,"networkId":1,"serviceId":2,"name":"x","type":1}]`, validServiceID, validServiceID), reason: provider.ReasonMalformed},
		{name: "missing required", body: `[{}]`, reason: provider.ReasonMalformed},
		{name: "wrong type", body: `[{"id":"1","networkId":1,"serviceId":2,"name":"x","type":1}]`, reason: provider.ReasonMalformed},
		{name: "overflow", body: `[{"id":1,"networkId":65536,"serviceId":2,"name":"x","type":1}]`, reason: provider.ReasonOverLimit},
		{name: "id mismatch", body: `[{"id":1,"networkId":1,"serviceId":2,"name":"x","type":1}]`, reason: provider.ReasonMalformed},
		{name: "trailing token", body: fmt.Sprintf(`[{"id":%d,"networkId":1,"serviceId":2,"name":"x","type":1}] {}`, validServiceID), reason: provider.ReasonMalformed},
		{name: "truncated", body: fmt.Sprintf(`[{"id":%d,"networkId":1,"serviceId":2,"name":"x","type":1}`, validServiceID), reason: provider.ReasonEarlyEOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, test.body) })
			defer server.Close()
			cursor, err := mustAdapter(t, server.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "test", Limit: 256})
			if err != nil {
				t.Fatal(err)
			}
			_, err = cursor.Next(context.Background())
			if !provider.IsReason(err, test.reason) {
				t.Fatalf("error=%v want=%s", err, test.reason)
			}
			if closeErr := cursor.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

func TestInvalidUTF8AndUnknownStructureLimits(t *testing.T) {
	t.Run("invalid utf8", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			body := append([]byte(`[{"id":100002,"networkId":1,"serviceId":2,"name":"`), 0xff)
			body = append(body, []byte(`","type":1}]`)...)
			_, _ = writer.Write(body)
		})
		defer server.Close()
		cursor, err := mustAdapter(t, server.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "utf8", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cursor.Next(context.Background())
		if !provider.IsReason(err, provider.ReasonMalformed) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("unknown token cap", func(t *testing.T) {
		values := strings.Repeat("0,", maxUnknownTokens) + "0"
		body := fmt.Sprintf(`[{"id":100002,"networkId":1,"serviceId":2,"name":"x","type":1,"unknown":[%s]}]`, values)
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, body) })
		defer server.Close()
		cursor, err := mustAdapter(t, server.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "tokens", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cursor.Next(context.Background())
		if !provider.IsReason(err, provider.ReasonOverLimit) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestHTTPFailureMappingAndRedirectPolicy(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, "private body")
			})
			defer server.Close()
			_, err := mustAdapter(t, server.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "status", Limit: 256})
			if err == nil || strings.Contains(err.Error(), "private body") {
				t.Fatalf("error=%v", err)
			}
		})
	}

	var targetCalls atomic.Int32
	target := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		writeJSON(writer, `[]`)
	})
	defer target.Close()
	redirect := newCatalogServer(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	})
	defer redirect.Close()
	_, err := mustAdapter(t, redirect.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "redirect", Limit: 256})
	if !provider.IsReason(err, provider.ReasonRejected) || targetCalls.Load() != 0 {
		t.Fatalf("error=%v target_calls=%d", err, targetCalls.Load())
	}
}

func TestContentTypeAndBodyCaps(t *testing.T) {
	t.Run("wrong content type", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(writer, `[]`)
		})
		defer server.Close()
		_, err := mustAdapter(t, server.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "type", Limit: 256})
		if !provider.IsReason(err, provider.ReasonMalformed) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("content length", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Content-Length", "1000")
			writer.WriteHeader(http.StatusOK)
		})
		defer server.Close()
		adapter := mustAdapter(t, server.URL)
		adapter.serviceCap = 64
		_, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "length", Limit: 256})
		if !provider.IsReason(err, provider.ReasonOverLimit) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("chunked over cap", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			flusher := writer.(http.Flusher)
			_, _ = io.WriteString(writer, `[`+strings.Repeat(" ", 128))
			flusher.Flush()
			_, _ = io.WriteString(writer, `]`)
		})
		defer server.Close()
		adapter := mustAdapter(t, server.URL)
		adapter.serviceCap = 64
		cursor, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "chunked", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cursor.Next(context.Background())
		if !provider.IsReason(err, provider.ReasonOverLimit) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestDeadlineDisconnectAndCancel(t *testing.T) {
	short := operationLimits{connectHeader: 25 * time.Millisecond, version: 25 * time.Millisecond, services: 25 * time.Millisecond, programs: 25 * time.Millisecond}
	t.Run("slow header", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			writeJSON(writer, `[]`)
		})
		defer server.Close()
		adapter, err := newAdapter(server.URL, short)
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "header", Limit: 256})
		if !provider.IsReason(err, provider.ReasonTimeout) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("stalled body", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `[`)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		})
		defer server.Close()
		adapter, err := newAdapter(server.URL, short)
		if err != nil {
			t.Fatal(err)
		}
		cursor, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "stall", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cursor.Next(context.Background())
		if !provider.IsReason(err, provider.ReasonTimeout) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("caller cancel", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `[`)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		})
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cursor, err := mustAdapter(t, server.URL).OpenServices(ctx, catalog.ServiceRequest{CorrelationID: "cancel", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		_, err = cursor.Next(ctx)
		if !provider.IsReason(err, provider.ReasonCancelled) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("mid-stream disconnect", func(t *testing.T) {
		server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Content-Length", "200")
			_, _ = io.WriteString(writer, `[{"id":100002`)
		})
		defer server.Close()
		cursor, err := mustAdapter(t, server.URL).OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "disconnect", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cursor.Next(context.Background())
		if err == nil {
			t.Fatal("切断が成功扱いになりました")
		}
	})
}

func TestCursorReturnsPageBeforeWholeArrayArrives(t *testing.T) {
	pageWritten := make(chan struct{})
	release := make(chan struct{})
	server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `[`)
		for index := 0; index < 256; index++ {
			if index != 0 {
				_, _ = io.WriteString(writer, `,`)
			}
			_, _ = fmt.Fprintf(writer, `{"id":%d,"networkId":1,"serviceId":%d,"name":"s","type":1}`, serviceProviderID(1, uint16(index)), index)
		}
		writer.(http.Flusher).Flush()
		close(pageWritten)
		<-release
		_, _ = fmt.Fprintf(writer, `,{"id":%d,"networkId":1,"serviceId":300,"name":"last","type":1}]`, serviceProviderID(1, 300))
	})
	defer server.Close()
	adapter := mustAdapter(t, server.URL)
	cursor, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "streaming", Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	<-pageWritten
	result := make(chan catalog.ServicePage, 1)
	errorsFound := make(chan error, 1)
	go func() {
		page, nextErr := cursor.Next(context.Background())
		result <- page
		errorsFound <- nextErr
	}()
	select {
	case page := <-result:
		if err := <-errorsFound; err != nil || len(page.Items) != 256 || page.End {
			t.Fatalf("page=%+v err=%v", page, err)
		}
	case <-time.After(time.Second):
		t.Fatal("最初のpageが配列終端を待ちました")
	}
	close(release)
	page, err := cursor.Next(context.Background())
	if err != nil || !page.End || len(page.Items) != 1 {
		t.Fatalf("last=%+v err=%v", page, err)
	}
}

func TestCountOneOverAndBodyClose(t *testing.T) {
	for _, test := range []struct {
		name   string
		total  int
		limit  int
		reason provider.Reason
	}{
		{name: "service boundary", total: provider.MaxServiceOperation - 1, limit: provider.MaxServiceOperation},
		{name: "service one over", total: provider.MaxServiceOperation, limit: provider.MaxServiceOperation, reason: provider.ReasonOverLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(`[ {"id":100002,"networkId":1,"serviceId":2,"name":"x","type":1} ]`))
			operation := newResponseOperation(context.Background(), func() {}, func() {}, body, 1024)
			if err := operation.beginArray(); err != nil {
				t.Fatal(err)
			}
			cursor := &serviceCursor{operation: operation, limit: 256, total: test.total, provenance: provider.Provenance{Backend: "MIRAKURUN", Revision: "test"}}
			page, err := cursor.Next(context.Background())
			if test.reason == "" {
				if err != nil || len(page.Items) != 1 || cursor.total != test.limit {
					t.Fatalf("page=%+v total=%d err=%v", page, cursor.total, err)
				}
			} else if !provider.IsReason(err, test.reason) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		total  int
		limit  int
		reason provider.Reason
	}{
		{name: "program boundary", total: provider.MaxProgramOperation - 1, limit: provider.MaxProgramOperation},
		{name: "program one over", total: provider.MaxProgramOperation, limit: provider.MaxProgramOperation, reason: provider.ReasonOverLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := io.NopCloser(strings.NewReader(`[ {"id":10000200003,"networkId":1,"serviceId":2,"eventId":3,"startAt":1785628800000,"duration":1000,"isFree":true,"name":"x"} ]`))
			operation := newResponseOperation(context.Background(), func() {}, func() {}, body, 1024)
			if err := operation.beginArray(); err != nil {
				t.Fatal(err)
			}
			cursor := &programCursor{operation: operation, limit: 256, total: test.total, provenance: provider.Provenance{Backend: "MIRAKURUN", Revision: "test"}}
			page, err := cursor.Next(context.Background())
			if test.reason == "" {
				if err != nil || len(page.Items) != 1 || cursor.total != test.limit {
					t.Fatalf("page=%+v total=%d err=%v", page, cursor.total, err)
				}
			} else if !provider.IsReason(err, test.reason) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	body := &trackingBody{Reader: strings.NewReader(`[]`)}
	adapter := mustAdapter(t, "http://127.0.0.1:40772")
	adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
	})
	cursor, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "close", Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cursor.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Close(); err != nil || !body.closed.Load() {
		t.Fatalf("closed=%v err=%v", body.closed.Load(), err)
	}
}

func TestConfigurationRejectsUnsafeEndpointAndProxy(t *testing.T) {
	for _, value := range []string{"", "http://host", "ftp://host:21", "http://user:pass@host:80", "http://host:80/path?query=1", "http://host:80/#fragment"} {
		if _, err := New(value); err == nil {
			t.Fatalf("unsafe URLが受理されました: %q", value)
		}
	}
	first := mustAdapter(t, "HTTP://Example.COM:40772/base/")
	second := mustAdapter(t, "http://example.com:40772/base")
	if first.IdentityHash() != second.IdentityHash() {
		t.Fatal("正規化後に同じendpointのidentityが一致しません")
	}
	transport, ok := first.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || !transport.DisableCompression || transport.MaxResponseHeaderBytes != headerLimit {
		t.Fatalf("transport=%+v", transport)
	}
}

func TestRepeatedCancellationDoesNotLeaveActiveRequest(t *testing.T) {
	server := newCatalogServer(t, func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, `[]`) })
	defer server.Close()
	adapter := mustAdapter(t, server.URL)
	before := runtime.NumGoroutine()
	for index := 0; index < 50; index++ {
		ctx, cancel := context.WithCancel(context.Background())
		cursor, err := adapter.OpenServices(ctx, catalog.ServiceRequest{CorrelationID: "repeat", Limit: 256})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		_ = cursor.Close()
	}
	adapter.CloseIdleConnections()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+8 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
	cursor, err := adapter.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "after", Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	_ = cursor.Close()
}

func newCatalogServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func writeJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(writer, body)
}

func mustAdapter(t *testing.T, baseURL string) *Adapter {
	t.Helper()
	adapter, err := New(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adapter.CloseIdleConnections)
	return adapter
}

type trackingBody struct {
	io.Reader
	closed atomic.Bool
}

func (body *trackingBody) Close() error {
	body.closed.Store(true)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

var _ http.RoundTripper = roundTripFunc(nil)
