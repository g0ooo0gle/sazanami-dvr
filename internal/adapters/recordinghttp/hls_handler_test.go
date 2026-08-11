package recordinghttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHLSHandlerRunsTvCastViewPlaylistAndRange(t *testing.T) {
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(stream)
	handler, err := NewHandlerWithLive(context.Background(), ownerOnlyTestRoot(t), &fakeHistory{}, &fakeFiles{},
		&fakeLogoCatalog{}, &fakeLogoProvider{}, live)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })

	tvCast := httptest.NewRequest(http.MethodGet, "/api/TvCast?"+validHLSTvCastQuery, nil)
	response := serveHLS(handler, tvCast)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		response.Header().Get("Content-Length") != "15" || response.Body.String() != `{"result":true}` {
		t.Fatalf("TvCast code=%d header=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if selected, opened, _ := live.counts(); selected != 1 || opened != 0 {
		t.Fatalf("TvCastでProviderを開きました: selected=%d opened=%d", selected, opened)
	}

	start := newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, validHLSViewBody)
	response = serveHLS(handler, start)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || response.Header().Get("Content-Type") != "" {
		t.Fatalf("POST view code=%d header=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	waitHLSSignal(t, stream.started)
	if _, opened, _ := live.counts(); opened != 1 {
		t.Fatalf("opened=%d", opened)
	}

	playlistRequest := newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")
	response = serveHLS(handler, playlistRequest)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" ||
		response.Header().Get("Cache-Control") != "no-store" || bytes.HasPrefix(response.Body.Bytes(), []byte{0xef, 0xbb, 0xbf}) ||
		!strings.Contains(response.Body.String(), "/komorebi/live/A_1-test/0.ts") {
		t.Fatalf("GET view code=%d header=%v body=%q", response.Code, response.Header(), response.Body.String())
	}

	segmentRequest := httptest.NewRequest(http.MethodGet, "/komorebi/live/A_1-test/0.ts", nil)
	segmentRequest.Header.Set("Cookie", "ctok=")
	full := serveHLS(handler, segmentRequest)
	if full.Code != http.StatusOK || full.Header().Get("Content-Type") != "video/mp2t" ||
		full.Header().Get("Accept-Ranges") != "bytes" || full.Body.Len() <= 188 {
		t.Fatalf("segment code=%d header=%v bytes=%d", full.Code, full.Header(), full.Body.Len())
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, "/komorebi/live/A_1-test/0.ts", nil)
	rangeRequest.Header.Set("Cookie", "ctok=")
	rangeRequest.Header.Set("Range", "bytes=188-")
	partial := serveHLS(handler, rangeRequest)
	if partial.Code != http.StatusPartialContent || !bytes.Equal(partial.Body.Bytes(), full.Body.Bytes()[188:]) {
		t.Fatalf("range code=%d header=%v bytes=%d", partial.Code, partial.Header(), partial.Body.Len())
	}
	multipleRange := httptest.NewRequest(http.MethodGet, "/komorebi/live/A_1-test/0.ts", nil)
	multipleRange.Header.Set("Cookie", "ctok=")
	multipleRange.Header.Set("Range", "bytes=0-1,4-5")
	response = serveHLS(handler, multipleRange)
	if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Body.String() != "multiple-ranges-unsupported\n" {
		t.Fatalf("multiple range code=%d body=%q", response.Code, response.Body.String())
	}

	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	waitHLSSignal(t, stream.done)
	if stream.closeCount() != 1 {
		t.Fatalf("stream close count=%d", stream.closeCount())
	}
}

func TestHLSHandlerRejectsBeforeStateAndRedactsInput(t *testing.T) {
	live := newFakeHLSSessionLive()
	handler, err := NewHandlerWithLive(context.Background(), ownerOnlyTestRoot(t), &fakeHistory{}, &fakeFiles{},
		&fakeLogoCatalog{}, &fakeLogoProvider{}, live)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })

	invalid := httptest.NewRequest(http.MethodGet, "/api/TvCast?id=1-2-3&n=0&json=1&ctok=&secret=private", nil)
	response := serveHLS(handler, invalid)
	if response.Code != http.StatusBadRequest || response.Body.String() != "invalid-query\n" ||
		strings.Contains(response.Body.String(), "private") {
		t.Fatalf("code=%d body=%q", response.Code, response.Body.String())
	}
	if selected, opened, closed := live.counts(); selected != 0 || opened != 0 || closed != 0 {
		t.Fatalf("invalid request touched state: %d/%d/%d", selected, opened, closed)
	}
	handler.hls.mu.Lock()
	handler.hls.keys["expired"] = struct{}{}
	handler.hls.mu.Unlock()
	expired := httptest.NewRequest(http.MethodGet, "/komorebi/live/expired/0.ts", nil)
	expired.Header.Set("Cookie", "ctok=")
	response = serveHLS(handler, expired)
	if response.Code != http.StatusGone || response.Body.String() != "session-ended\n" {
		t.Fatalf("expired code=%d body=%q", response.Code, response.Body.String())
	}

	method := httptest.NewRequest(http.MethodPost, "/api/TvCast?"+validHLSTvCastQuery, nil)
	response = serveHLS(handler, method)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method code=%d header=%v", response.Code, response.Header())
	}

	oldHandler, err := NewHandler(&fakeHistory{}, &fakeFiles{})
	if err != nil {
		t.Fatal(err)
	}
	response = serveHLS(oldHandler, httptest.NewRequest(http.MethodGet, "/api/TvCast?"+validHLSTvCastQuery, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("旧constructorがHLSを公開しました: code=%d", response.Code)
	}
}

func serveHLS(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
