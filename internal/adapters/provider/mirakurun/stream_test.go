package mirakurun

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
