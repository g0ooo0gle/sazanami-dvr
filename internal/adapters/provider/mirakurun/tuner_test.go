package mirakurun

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

func TestObserveTunerCountCountsEachObjectOnce(t *testing.T) {
	for _, want := range []int{1, 19, 20, 21} {
		t.Run(fmt.Sprint(want), func(t *testing.T) {
			items := make([]string, want)
			for index := range items {
				items[index] = fmt.Sprintf(`{"name":"private-%d","types":["GR","BS","CS"],"isFree":false,"isUsing":true,"isFault":true,"isRemote":true,"future":{"nested":[1,true,null]}}`, index)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Method != http.MethodGet || request.URL.Path != "/base/api/tuners" ||
					request.Header.Get("Accept") != "application/json" {
					t.Errorf("request=%s %s accept=%q", request.Method, request.URL.Path, request.Header.Get("Accept"))
				}
				writeJSON(writer, "["+strings.Join(items, ",")+"]")
			}))
			defer server.Close()
			adapter := mustAdapter(t, server.URL+"/base")
			got, err := adapter.ObserveTunerCount(context.Background())
			if err != nil || got != want || calls.Load() != 1 {
				t.Fatalf("count=%d want=%d calls=%d err=%v", got, want, calls.Load(), err)
			}
		})
	}
}

func TestObserveTunerCountRejectsInvalidJSON(t *testing.T) {
	deep := strings.Repeat("[", maxJSONDepth+2) + "0" + strings.Repeat("]", maxJSONDepth+2)
	tokens := strings.Repeat("0,", maxUnknownTokens) + "0"
	tests := []struct {
		name   string
		body   []byte
		reason provider.Reason
	}{
		{name: "empty", body: []byte(`[]`), reason: provider.ReasonMalformed},
		{name: "top level object", body: []byte(`{}`), reason: provider.ReasonMalformed},
		{name: "non object", body: []byte(`[1]`), reason: provider.ReasonMalformed},
		{name: "duplicate key", body: []byte(`[{"name":"a","name":"b"}]`), reason: provider.ReasonMalformed},
		{name: "invalid utf8", body: append([]byte(`[{"name":"`), append([]byte{0xff}, []byte(`"}]`)...)...), reason: provider.ReasonMalformed},
		{name: "depth", body: []byte(`[{"future":` + deep + `}]`), reason: provider.ReasonOverLimit},
		{name: "tokens", body: []byte(`[{"future":[` + tokens + `]}]`), reason: provider.ReasonOverLimit},
		{name: "trailing", body: []byte(`[{}] {}`), reason: provider.ReasonMalformed},
		{name: "truncated", body: []byte(`[{}`), reason: provider.ReasonEarlyEOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write(test.body)
			}))
			defer server.Close()
			_, err := mustAdapter(t, server.URL).ObserveTunerCount(context.Background())
			if !provider.IsReason(err, test.reason) {
				t.Fatalf("error=%v want=%s", err, test.reason)
			}
		})
	}
}

func TestObserveTunerCountEnforcesHTTPBoundary(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, "private response")
			}))
			defer server.Close()
			_, err := mustAdapter(t, server.URL).ObserveTunerCount(context.Background())
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("status=%d error=%v", status, err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		header http.Header
	}{
		{name: "content type", header: http.Header{"Content-Type": {"text/plain"}}},
		{name: "compression", header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := mustAdapter(t, "http://127.0.0.1:40772")
			body := &trackingBody{Reader: strings.NewReader(`[{}]`)}
			adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: test.header, Body: body}, nil
			})
			if _, err := adapter.ObserveTunerCount(context.Background()); err == nil || !body.closed.Load() {
				t.Fatalf("error=%v closed=%v", err, body.closed.Load())
			}
		})
	}

	adapter := mustAdapter(t, "http://127.0.0.1:40772")
	adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}}, nil
	})
	if _, err := adapter.ObserveTunerCount(context.Background()); err == nil {
		t.Fatalf("missing body error=%v", err)
	}
}

func TestObserveTunerCountEnforcesBodyCap(t *testing.T) {
	padded := func(size int) string { return "[{}" + strings.Repeat(" ", size-4) + "]" }
	for _, test := range []struct {
		name    string
		size    int
		chunked bool
		wantErr bool
	}{
		{name: "content length exact", size: 64},
		{name: "content length one over", size: 65, wantErr: true},
		{name: "chunked exact", size: 64, chunked: true},
		{name: "chunked one over", size: 65, chunked: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				body := padded(test.size)
				if test.chunked {
					_, _ = io.WriteString(writer, body[:3])
					writer.(http.Flusher).Flush()
					_, _ = io.WriteString(writer, body[3:])
					return
				}
				writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
				_, _ = io.WriteString(writer, body)
			}))
			defer server.Close()
			adapter := mustAdapter(t, server.URL)
			adapter.tunerCap = 64
			count, err := adapter.ObserveTunerCount(context.Background())
			if test.wantErr {
				if !provider.IsReason(err, provider.ReasonOverLimit) {
					t.Fatalf("count=%d error=%v", count, err)
				}
			} else if err != nil || count != 1 {
				t.Fatalf("count=%d error=%v", count, err)
			}
		})
	}
}

func TestObserveTunerCountDeadlineDisconnectAndCancel(t *testing.T) {
	short := productionLimits
	short.connectHeader = 25 * time.Millisecond
	short.tuners = 25 * time.Millisecond
	for _, test := range []struct {
		name string
		body http.HandlerFunc
	}{
		{name: "header stall", body: func(writer http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			writeJSON(writer, `[{}]`)
		}},
		{name: "body stall", body: func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `[`)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.body)
			defer server.Close()
			adapter, err := newAdapter(server.URL, short)
			if err != nil {
				t.Fatal(err)
			}
			defer adapter.CloseIdleConnections()
			if _, err := adapter.ObserveTunerCount(context.Background()); !provider.IsReason(err, provider.ReasonTimeout) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(writer, `[{`)
	}))
	_, err := mustAdapter(t, server.URL).ObserveTunerCount(context.Background())
	server.Close()
	if err == nil {
		t.Fatal("途中切断が成功扱いになりました")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	if _, err := mustAdapter(t, "http://"+address).ObserveTunerCount(context.Background()); err == nil {
		t.Fatal("接続失敗が成功扱いになりました")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mustAdapter(t, "http://127.0.0.1:40772").ObserveTunerCount(ctx); !provider.IsReason(err, provider.ReasonCancelled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestObserveTunerCountClosesBodyAndDoesNotRetainRequests(t *testing.T) {
	adapter := mustAdapter(t, "http://127.0.0.1:40772")
	var bodies []*trackingBody
	adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := &trackingBody{Reader: strings.NewReader("[" + strings.Repeat("{},", 9_999) + "{}]")}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}, nil
	})
	before := runtime.NumGoroutine()
	for range 20 {
		count, err := adapter.ObserveTunerCount(context.Background())
		if err != nil || count != 10_000 {
			t.Fatalf("count=%d error=%v", count, err)
		}
	}
	for index, body := range bodies {
		if !body.closed.Load() {
			t.Fatalf("body %d is open", index)
		}
	}
	if adapter.active {
		t.Fatal("adapterに処理中状態が残りました")
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+8 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
}
