package recordinghttp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
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
	for _, test := range []struct {
		request *http.Request
		allow   string
	}{
		{request: httptest.NewRequest(http.MethodPut, "/api/view?"+validHLSViewQuery, nil), allow: "GET, POST"},
		{request: httptest.NewRequest(http.MethodHead, "/komorebi/live/A/0.ts", nil), allow: http.MethodGet},
	} {
		response = serveHLS(handler, test.request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != test.allow {
			t.Fatalf("method=%s code=%d allow=%q", test.request.Method, response.Code, response.Header().Get("Allow"))
		}
	}

	live.openErr = errors.New("private provider detail")
	response = serveHLS(handler, httptest.NewRequest(http.MethodGet, "/api/TvCast?"+validHLSTvCastQuery, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("select code=%d body=%q", response.Code, response.Body.String())
	}
	response = serveHLS(handler, newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, validHLSViewBody))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "live-provider-unavailable\n" ||
		strings.Contains(response.Body.String(), "private") {
		t.Fatalf("provider failure code=%d body=%q", response.Code, response.Body.String())
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

func TestHLSHandlerSharesStreamLimitWithRecordingRoutes(t *testing.T) {
	files := &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)}
	handler, segment := newHLSHandlerWithReadableSegment(t, files)
	started := make(chan struct{}, maximumStreams+1)
	done := make(chan struct{}, maximumStreams+1)
	releases := make([]chan struct{}, maximumStreams)
	for index := range maximumStreams {
		releases[index] = make(chan struct{})
		path := "/recordings/7.ts"
		if index%2 == 1 {
			path = "/api/xcode?id=7&option=10"
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		writer := &blockingResponseWriter{header: make(http.Header), started: started, release: releases[index]}
		go func() {
			handler.ServeHTTP(writer, request)
			done <- struct{}{}
		}()
	}
	t.Cleanup(func() {
		for _, release := range releases {
			select {
			case <-release:
			default:
				close(release)
			}
		}
	})
	for range maximumStreams {
		waitHLSSignal(t, started)
	}
	if len(handler.streams) != maximumStreams || files.opened.Load() != maximumStreams || hlsSegmentReaders(segment) != 0 {
		t.Fatalf("slots=%d opened=%d readers=%d", len(handler.streams), files.opened.Load(), hlsSegmentReaders(segment))
	}

	response := serveHLS(handler, newHLSSegmentRequest(context.Background()))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "stream-limit\n" ||
		files.opened.Load() != maximumStreams || hlsSegmentReaders(segment) != 0 {
		t.Fatalf("full code=%d body=%q opened=%d readers=%d", response.Code, response.Body.String(),
			files.opened.Load(), hlsSegmentReaders(segment))
	}

	close(releases[0])
	waitHLSSignal(t, done)
	response = serveHLS(handler, newHLSSegmentRequest(context.Background()))
	if response.Code != http.StatusOK || hlsSegmentReaders(segment) != 0 || len(handler.streams) != maximumStreams-1 {
		t.Fatalf("reused code=%d slots=%d readers=%d", response.Code, len(handler.streams), hlsSegmentReaders(segment))
	}

	hlsRelease := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-hlsRelease:
		default:
			close(hlsRelease)
		}
	})
	go func() {
		handler.ServeHTTP(&blockingResponseWriter{header: make(http.Header), started: started, release: hlsRelease},
			newHLSSegmentRequest(context.Background()))
		done <- struct{}{}
	}()
	waitHLSSignal(t, started)
	if len(handler.streams) != maximumStreams || hlsSegmentReaders(segment) != 1 {
		t.Fatalf("mixed slots=%d readers=%d", len(handler.streams), hlsSegmentReaders(segment))
	}
	response = serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", nil)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "stream-limit\n" ||
		files.opened.Load() != maximumStreams || hlsSegmentReaders(segment) != 1 {
		t.Fatalf("mixed full code=%d body=%q opened=%d readers=%d", response.Code, response.Body.String(),
			files.opened.Load(), hlsSegmentReaders(segment))
	}

	close(hlsRelease)
	waitHLSSignal(t, done)
	for index := 1; index < maximumStreams; index++ {
		close(releases[index])
	}
	for index := 1; index < maximumStreams; index++ {
		waitHLSSignal(t, done)
	}
	if len(handler.streams) != 0 || hlsSegmentReaders(segment) != 0 ||
		files.opened.Load() != maximumStreams || files.closed.Load() != maximumStreams {
		t.Fatalf("final slots=%d readers=%d opened=%d closed=%d", len(handler.streams), hlsSegmentReaders(segment),
			files.opened.Load(), files.closed.Load())
	}
}

func TestHLSHandlerReleasesStreamSlotAfterCancellationAndWriteFailure(t *testing.T) {
	handler, segment := newHLSHandlerWithReadableSegment(t, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})

	cancelledContext, cancelBefore := context.WithCancel(context.Background())
	cancelBefore()
	response := serveHLS(handler, newHLSSegmentRequest(cancelledContext))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "request-cancelled\n" ||
		len(handler.streams) != 0 || hlsSegmentReaders(segment) != 0 {
		t.Fatalf("cancel before code=%d body=%q slots=%d readers=%d", response.Code, response.Body.String(),
			len(handler.streams), hlsSegmentReaders(segment))
	}

	requestContext, cancelDuring := context.WithCancel(context.Background())
	writer := &cancelingResponseWriter{header: make(http.Header), cancel: cancelDuring}
	handler.ServeHTTP(writer, newHLSSegmentRequest(requestContext))
	cancelDuring()
	if writer.writes != 1 || len(handler.streams) != 0 || hlsSegmentReaders(segment) != 0 {
		t.Fatalf("cancel during writes=%d slots=%d readers=%d", writer.writes, len(handler.streams), hlsSegmentReaders(segment))
	}

	handler.ServeHTTP(&failingResponseWriter{header: make(http.Header)}, newHLSSegmentRequest(context.Background()))
	if len(handler.streams) != 0 || hlsSegmentReaders(segment) != 0 {
		t.Fatalf("write failure slots=%d readers=%d", len(handler.streams), hlsSegmentReaders(segment))
	}
	response = serveHLS(handler, newHLSSegmentRequest(context.Background()))
	if response.Code != http.StatusOK || len(handler.streams) != 0 || hlsSegmentReaders(segment) != 0 {
		t.Fatalf("after failure code=%d slots=%d readers=%d", response.Code, len(handler.streams), hlsSegmentReaders(segment))
	}
}

func newHLSHandlerWithReadableSegment(t *testing.T, files *fakeFiles) (*Handler, *hlsCacheSegment) {
	t.Helper()
	live := newFakeHLSSessionLive()
	stream := newFakeHLSSessionStream(testHLSSessionTS(t, 4), nil, true)
	live.queue(stream)
	handler, err := NewHandlerWithLive(context.Background(), ownerOnlyTestRoot(t),
		&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, files,
		&fakeLogoCatalog{}, &fakeLogoProvider{}, live)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	if response := serveHLS(handler, httptest.NewRequest(http.MethodGet, "/api/TvCast?"+validHLSTvCastQuery, nil)); response.Code != http.StatusOK {
		t.Fatalf("select code=%d body=%q", response.Code, response.Body.String())
	}
	if response := serveHLS(handler, newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, validHLSViewBody)); response.Code != http.StatusNoContent {
		t.Fatalf("start code=%d body=%q", response.Code, response.Body.String())
	}
	waitHLSSignal(t, stream.started)
	if response := serveHLS(handler, newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")); response.Code != http.StatusOK {
		t.Fatalf("playlist code=%d body=%q", response.Code, response.Body.String())
	}
	handler.hls.mu.Lock()
	session := handler.hls.sessions["A_1-test"]
	handler.hls.mu.Unlock()
	if session == nil {
		t.Fatal("HLS session was not published")
	}
	cache := session.generation.cache
	cache.mu.Lock()
	segment := session.generation.segments[0]
	cache.mu.Unlock()
	if segment == nil {
		t.Fatal("HLS segment was not published")
	}
	return handler, segment
}

func newHLSSegmentRequest(ctx context.Context) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/komorebi/live/A_1-test/0.ts", nil).WithContext(ctx)
	request.Header.Set("Cookie", "ctok=")
	return request
}

func hlsSegmentReaders(segment *hlsCacheSegment) int {
	cache := segment.generation.cache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return segment.readers
}

func serveHLS(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
