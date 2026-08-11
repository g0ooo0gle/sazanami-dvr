package mirakurun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

func TestStreamRequestAndChunkedRead(t *testing.T) {
	requestEnded := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/services/1003/stream" ||
			request.URL.RawQuery != "decode=1" || request.Header.Get("X-Mirakurun-Priority") != "0" ||
			request.Header.Get("Accept") != "video/MP2T" {
			t.Errorf("request=%s %s?%s headers=%v", request.Method, request.URL.Path, request.URL.RawQuery, request.Header)
		}
		writer.Header().Set("Content-Type", "video/MP2T; charset=binary")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(requestEnded)
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := adapter.client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.ForceAttemptHTTP2 || len(transport.TLSNextProto) != 0 {
		t.Fatalf("unsafe transport=%+v", transport)
	}
	lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, provider.MaxStreamChunk)
	read, terminal, err := lease.Read(context.Background(), buffer)
	if err != nil || read != 188 || terminal.Done || !bytes.Equal(buffer[:read], bytes.Repeat([]byte{0x47}, 188)) {
		t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
	}
	if err := lease.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-requestEnded:
	case <-time.After(time.Second):
		t.Fatal("request bodyがcancel後も開いたままです")
	}
}

func TestStreamAcceptsIndependentLiveUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamWithLimit(server.URL, 4)
	if err != nil {
		t.Fatal(err)
	}
	request := validStreamRequest("1003")
	request.Usage = providerstream.UsageLive
	request.CorrelationID = "live-test"
	lease, err := adapter.OpenStream(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Cancel(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamRejectsStatusContentTypeAndRedirect(t *testing.T) {
	redirectTarget := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/services/300/stream":
			http.Redirect(writer, request, "/target", http.StatusFound)
		case "/api/services/404/stream":
			http.Error(writer, "private body", http.StatusNotFound)
		case "/api/services/500/stream":
			http.Error(writer, "private body", http.StatusInternalServerError)
		case "/api/services/200/stream":
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.WriteHeader(http.StatusOK)
		case "/target":
			redirectTarget.Add(1)
		}
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		id     string
		reason provider.Reason
	}{
		{id: "300", reason: provider.ReasonRejected},
		{id: "404", reason: provider.ReasonNotFound},
		{id: "500", reason: provider.ReasonUnavailable},
		{id: "200", reason: provider.ReasonMalformed},
	} {
		if _, err := adapter.OpenStream(context.Background(), validStreamRequest(test.id)); !provider.IsReason(err, test.reason) {
			t.Fatalf("id=%s err=%v", test.id, err)
		}
	}
	if redirectTarget.Load() != 0 {
		t.Fatal("redirect先へ接続しました")
	}
}

func TestStreamReadTimeoutDisconnectAndCancel(t *testing.T) {
	t.Run("idle timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()
		adapter, err := newStreamAdapter(server.URL, streamLimits{connectHeader: time.Second, readIdle: 40 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		read, terminal, err := lease.Read(context.Background(), make([]byte, 188))
		if read != 0 || terminal.Reason != providerstream.TerminalTimeout || !provider.IsReason(err, provider.ReasonTimeout) ||
			time.Since(started) < 20*time.Millisecond || time.Since(started) > time.Second {
			t.Fatalf("read=%d terminal=%+v elapsed=%v err=%v", read, terminal, time.Since(started), err)
		}
		_ = lease.Close()
	})

	t.Run("truncated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.Header().Set("Content-Length", "376")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
		}))
		defer server.Close()
		adapter, _ := NewStream(server.URL)
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatal(err)
		}
		read, terminal, err := lease.Read(context.Background(), make([]byte, 376))
		if err == nil && !terminal.Done {
			second, secondTerminal, secondErr := lease.Read(context.Background(), make([]byte, 376))
			read += second
			terminal, err = secondTerminal, secondErr
		}
		if read != 188 || terminal.Reason != providerstream.TerminalPeer || err == nil {
			t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
		}
		_ = lease.Close()
	})

	t.Run("cancel", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}))
		defer server.Close()
		adapter, _ := NewStream(server.URL)
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			_, terminal, readErr := lease.Read(context.Background(), make([]byte, 188))
			if terminal.Reason != providerstream.TerminalCancelled {
				result <- errors.New("cancelled終端ではありません")
				return
			}
			result <- readErr
		}()
		time.Sleep(20 * time.Millisecond)
		if err := lease.Cancel(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if !provider.IsReason(err, provider.ReasonCancelled) {
				t.Fatalf("err=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelでreadが解除されません")
		}
	})
}

func TestStreamCanOpenAgainAfterTemporaryFailures(t *testing.T) {
	requests := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1:
			http.Error(writer, "private body", http.StatusServiceUnavailable)
		case 2:
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.Header().Set("Content-Length", "376")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
		default:
			writer.Header().Set("Content-Type", "video/MP2T")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
			writer.(http.Flusher).Flush()
			<-request.Context().Done()
		}
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.OpenStream(context.Background(), validStreamRequest("1003")); !provider.IsReason(err, provider.ReasonUnavailable) {
		t.Fatalf("5xx err=%v", err)
	}
	lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
	if err != nil {
		t.Fatal(err)
	}
	read, terminal, err := streamTestReadTerminal(lease)
	if read != 188 || !terminal.Done || err == nil {
		t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	lease, err = adapter.OpenStream(context.Background(), validStreamRequest("1003"))
	if err != nil {
		t.Fatal(err)
	}
	read, terminal, err = lease.Read(context.Background(), make([]byte, 188))
	if err != nil || read != 188 || terminal.Done {
		t.Fatalf("read=%d terminal=%+v err=%v", read, terminal, err)
	}
	_ = lease.Cancel()
	_ = lease.Close()
	if requests.Load() != 3 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestStreamUsesExplicitConcurrentLimitAndReusesSlot(t *testing.T) {
	var requests atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamWithLimit(server.URL, 2)
	if err != nil {
		t.Fatal(err)
	}
	transport := adapter.client.Transport.(*http.Transport)
	if transport.MaxConnsPerHost != 2 || transport.MaxIdleConnsPerHost != 2 || transport.MaxIdleConns != 2 {
		t.Fatalf("transport limits=%d/%d/%d", transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost, transport.MaxIdleConns)
	}
	firstRequest := validStreamRequest("1003")
	firstRequest.CorrelationID = "recording-first"
	first, err := adapter.OpenStream(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := validStreamRequest("1004")
	secondRequest.CorrelationID = "recording-second"
	second, err := adapter.OpenStream(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	thirdRequest := validStreamRequest("1005")
	thirdRequest.CorrelationID = "recording-third"
	if _, err := adapter.OpenStream(context.Background(), thirdRequest); !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("上限超過err=%v", err)
	}
	if requests.Load() != 2 || maximum.Load() != 2 {
		t.Fatalf("requests=%d maximum=%d", requests.Load(), maximum.Load())
	}
	_ = first.Cancel()
	_ = first.Close()
	third, err := adapter.OpenStream(context.Background(), thirdRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Cancel()
	_ = second.Close()
	_ = third.Cancel()
	_ = third.Close()
	adapter.mu.Lock()
	remaining := adapter.active
	adapter.mu.Unlock()
	if requests.Load() != 3 || remaining != 0 {
		t.Fatalf("requests=%d remaining=%d", requests.Load(), remaining)
	}
}

func TestStreamOpensNineIndependentRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	adapter, err := NewStreamWithLimit(server.URL, 9)
	if err != nil {
		t.Fatal(err)
	}
	leases := make([]providerstream.Lease, 0, 9)
	for index := range 9 {
		request := validStreamRequest(fmt.Sprint(1000 + index))
		request.CorrelationID = fmt.Sprintf("recording-%d", index)
		lease, err := adapter.OpenStream(context.Background(), request)
		if err != nil {
			t.Fatalf("index=%d error=%v", index, err)
		}
		leases = append(leases, lease)
	}
	over := validStreamRequest("2000")
	over.CorrelationID = "recording-over"
	if _, err := adapter.OpenStream(context.Background(), over); !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("上限超過error=%v", err)
	}
	if requests.Load() != 9 || adapter.active != 9 {
		t.Fatalf("requests=%d active=%d", requests.Load(), adapter.active)
	}
	for _, lease := range leases {
		_ = lease.Cancel()
		_ = lease.Close()
	}
	if adapter.active != 0 {
		t.Fatalf("active=%d", adapter.active)
	}
}

func TestStreamAcceptsAnyPositiveConcurrentLimit(t *testing.T) {
	for _, maximum := range []int{-1, 0} {
		if _, err := NewStreamWithLimit("http://127.0.0.1:9", maximum); !provider.IsReason(err, provider.ReasonInternal) {
			t.Fatalf("maximum=%d err=%v", maximum, err)
		}
	}
	for _, maximum := range []int{9, 20, 1_000_000_000} {
		adapter, err := NewStreamWithLimit("http://127.0.0.1:9", maximum)
		if err != nil {
			t.Fatalf("maximum=%d err=%v", maximum, err)
		}
		transport := adapter.client.Transport.(*http.Transport)
		if adapter.active != 0 || transport.MaxConnsPerHost != maximum || transport.MaxIdleConnsPerHost != maximum {
			t.Fatalf("maximum=%d active=%d transport=%d/%d", maximum, adapter.active,
				transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost)
		}
	}
}

func TestStreamRepeatedDisconnectDoesNotLeakResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "video/MP2T")
		writer.Header().Set("Content-Length", "376")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(bytes.Repeat([]byte{0x47}, 188))
	}))
	defer server.Close()
	adapter, err := NewStream(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs, measureFDs := streamTestFDCount()
	for cycle := 0; cycle < 100; cycle++ {
		lease, err := adapter.OpenStream(context.Background(), validStreamRequest("1003"))
		if err != nil {
			t.Fatalf("cycle=%d open=%v", cycle, err)
		}
		read, terminal, err := streamTestReadTerminal(lease)
		if read != 188 || !terminal.Done || err == nil {
			t.Fatalf("cycle=%d read=%d terminal=%+v err=%v", cycle, read, terminal, err)
		}
		if err := lease.Close(); err != nil {
			t.Fatalf("cycle=%d close=%v", cycle, err)
		}
	}
	adapter.CloseIdleConnections()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > beforeGoroutines+8 {
		t.Fatalf("goroutines before=%d after=%d", beforeGoroutines, after)
	}
	if after, supported := streamTestFDCount(); measureFDs && supported && after > beforeFDs+4 {
		t.Fatalf("file descriptors before=%d after=%d", beforeFDs, after)
	}
}

func TestStreamRequestValidationAndBufferCap(t *testing.T) {
	adapter, err := NewStream("http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "0", "01", "-1", "abc"} {
		if _, err := adapter.OpenStream(context.Background(), validStreamRequest(id)); !provider.IsReason(err, provider.ReasonRejected) {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}
	lease := &streamLease{}
	if _, _, err := lease.Read(context.Background(), make([]byte, provider.MaxStreamChunk+1)); !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("err=%v", err)
	}
}

func validStreamRequest(id string) providerstream.Request {
	return providerstream.Request{
		Target: provider.TuningTarget{Opaque: id}, Usage: providerstream.UsageRecording,
		PriorityPolicy: "0", RequireDescrambled: true, CorrelationID: "recording-test",
	}
}

func streamTestFDCount() (int, bool) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

func streamTestReadTerminal(lease providerstream.Lease) (int, providerstream.Terminal, error) {
	total := 0
	for range 2 {
		read, terminal, err := lease.Read(context.Background(), make([]byte, 376))
		total += read
		if terminal.Done || err != nil {
			return total, terminal, err
		}
	}
	return total, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}
