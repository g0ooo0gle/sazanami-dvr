package recordinghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestResolverOffersOnlyOriginalXcodeAndKeepsRecordingURL(t *testing.T) {
	handler, err := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})
	if err != nil {
		t.Fatal(err)
	}
	get := serve(handler, http.MethodGet, "/komorebi/resolver.lua", nil)
	if get.Code != http.StatusOK || get.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		get.Header().Get("Content-Length") != strconv.Itoa(get.Body.Len()) {
		t.Fatalf("get=%d header=%v body=%q", get.Code, get.Header(), get.Body.String())
	}
	var settings struct {
		Token struct {
			Xcode string `json:"xcode"`
			View  string `json:"view"`
		} `json:"ctok"`
		Options []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"option"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Token.Xcode != "" || settings.Token.View != "" || len(settings.Options) != 1 ||
		settings.Options[0].ID != "10" || settings.Options[0].Name != "オリジナル（変換なし）" {
		t.Fatalf("settings=%+v", settings)
	}
	head := serve(handler, http.MethodHead, "/komorebi/resolver.lua", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Type") != get.Header().Get("Content-Type") ||
		head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
		t.Fatalf("head=%d header=%v body=%q", head.Code, head.Header(), head.Body.String())
	}
	item := serve(handler, http.MethodGet, "/komorebi/resolver.lua?id=7", nil)
	if item.Code != http.StatusOK || item.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		!bytes.Contains(item.Body.Bytes(), []byte(`"video_url":"/recordings/7.ts"`)) ||
		bytes.Contains(item.Body.Bytes(), []byte("/api/xcode")) {
		t.Fatalf("item=%d body=%q", item.Code, item.Body.String())
	}
	itemHead := serve(handler, http.MethodHead, "/komorebi/resolver.lua?id=7", nil)
	if itemHead.Code != item.Code || itemHead.Header().Get("Content-Type") != item.Header().Get("Content-Type") || itemHead.Body.Len() != 0 {
		t.Fatalf("item head=%d header=%v body=%q", itemHead.Code, itemHead.Header(), itemHead.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if response := serve(handler, method, "/komorebi/resolver.lua?id=8", nil); response.Code != http.StatusNotFound {
			t.Fatalf("unknown method=%s code=%d body=%q", method, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{
		"/komorebi/resolver.lua?unknown=1",
		"/komorebi/resolver.lua?id=7&id=8",
		"/komorebi/resolver.lua?id=7&unknown=1",
	} {
		if response := serve(handler, http.MethodGet, path, nil); response.Code != http.StatusNotFound {
			t.Fatalf("path=%q code=%d body=%q", path, response.Code, response.Body.String())
		}
	}
}

func TestParseXcodeQueryContract(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		wantID int32
		reason string
	}{
		{name: "id", raw: "id=7&option=10", wantID: 7},
		{name: "fname", raw: "fname=recordings/7.ts&option=10", wantID: 7},
		{name: "encoded fname", raw: "fname=recordings%2F7.ts&option=10", wantID: 7},
		{name: "maximum id", raw: "id=2147483647&option=10", wantID: 2147483647},
		{name: "maximum fname", raw: "fname=recordings/2147483647.ts&option=10", wantID: 2147483647},
		{name: "empty token", raw: "id=7&option=10&ctok=", wantID: 7},
		{name: "zero offset", raw: "id=7&option=10&ofssec=0", wantID: 7},
		{name: "maximum offset", raw: "id=7&option=10&ofssec=86400", wantID: 7},

		{name: "empty", raw: "", reason: "invalid-query"},
		{name: "selector missing", raw: "option=10", reason: "invalid-query"},
		{name: "selectors conflict", raw: "id=7&fname=recordings/7.ts&option=10", reason: "invalid-query"},
		{name: "duplicate id", raw: "id=7&id=8&option=10", reason: "invalid-query"},
		{name: "encoded duplicate id", raw: "i%64=7&id=8&option=10", reason: "invalid-query"},
		{name: "duplicate fname", raw: "fname=recordings/7.ts&fname=recordings/8.ts&option=10", reason: "invalid-query"},
		{name: "unknown", raw: "id=7&option=10&unknown=1", reason: "invalid-query"},
		{name: "empty name", raw: "id=7&option=10&=1", reason: "invalid-query"},
		{name: "encoded empty name", raw: "id=7&option=10&%00=1", reason: "invalid-query"},
		{name: "empty field", raw: "id=7&&option=10", reason: "invalid-query"},
		{name: "trailing field", raw: "id=7&option=10&", reason: "invalid-query"},
		{name: "malformed escape", raw: "id=7&option=10&ctok=%ZZ", reason: "invalid-query"},
		{name: "semicolon separator", raw: "id=7;option=10", reason: "invalid-query"},

		{name: "fname empty", raw: "fname=&option=10", reason: "invalid-query"},
		{name: "fname leading slash", raw: "fname=/recordings/7.ts&option=10", reason: "invalid-query"},
		{name: "fname empty element", raw: "fname=recordings//7.ts&option=10", reason: "invalid-query"},
		{name: "fname dot", raw: "fname=recordings/./7.ts&option=10", reason: "invalid-query"},
		{name: "fname parent", raw: "fname=recordings/../7.ts&option=10", reason: "invalid-query"},
		{name: "fname backslash", raw: "fname=recordings%5C7.ts&option=10", reason: "invalid-query"},
		{name: "fname nul", raw: "fname=recordings/7%00.ts&option=10", reason: "invalid-query"},
		{name: "fname absolute", raw: "fname=/private/recordings/7.ts&option=10", reason: "invalid-query"},
		{name: "fname url", raw: "fname=http%3A%2F%2Fhost%2Frecordings%2F7.ts&option=10", reason: "invalid-query"},
		{name: "fname edcb", raw: "fname=video%2Frec%2F7.ts&option=10", reason: "invalid-query"},
		{name: "fname trailing", raw: "fname=recordings/7.ts.tmp&option=10", reason: "invalid-query"},
		{name: "fname zero", raw: "fname=recordings/0.ts&option=10", reason: "invalid-query"},
		{name: "fname negative", raw: "fname=recordings/-1.ts&option=10", reason: "invalid-query"},
		{name: "fname leading zero", raw: "fname=recordings/07.ts&option=10", reason: "invalid-query"},
		{name: "fname overflow", raw: "fname=recordings/2147483648.ts&option=10", reason: "invalid-query"},
		{name: "id zero", raw: "id=0&option=10", reason: "invalid-query"},
		{name: "id negative", raw: "id=-1&option=10", reason: "invalid-query"},
		{name: "id sign", raw: "id=%2B7&option=10", reason: "invalid-query"},
		{name: "id leading zero", raw: "id=07&option=10", reason: "invalid-query"},
		{name: "id overflow", raw: "id=2147483648&option=10", reason: "invalid-query"},

		{name: "option missing", raw: "id=7", reason: "unsupported-option"},
		{name: "option empty", raw: "id=7&option=", reason: "unsupported-option"},
		{name: "option duplicate", raw: "id=7&option=10&option=10", reason: "unsupported-option"},
		{name: "option other", raw: "id=7&option=2", reason: "unsupported-option"},
		{name: "option sign", raw: "id=7&option=%2B10", reason: "unsupported-option"},
		{name: "option leading zero", raw: "id=7&option=010", reason: "unsupported-option"},
		{name: "option overflow", raw: "id=7&option=18446744073709551616", reason: "unsupported-option"},
		{name: "token nonempty", raw: "id=7&option=10&ctok=secret", reason: "invalid-token"},
		{name: "token duplicate", raw: "id=7&option=10&ctok=&ctok=", reason: "invalid-token"},
		{name: "token nul", raw: "id=7&option=10&ctok=%00", reason: "invalid-token"},
		{name: "offset empty", raw: "id=7&option=10&ofssec=", reason: "invalid-offset"},
		{name: "offset duplicate", raw: "id=7&option=10&ofssec=1&ofssec=2", reason: "invalid-offset"},
		{name: "offset over maximum", raw: "id=7&option=10&ofssec=86401", reason: "invalid-offset"},
		{name: "offset negative", raw: "id=7&option=10&ofssec=-1", reason: "invalid-offset"},
		{name: "offset sign", raw: "id=7&option=10&ofssec=%2B1", reason: "invalid-offset"},
		{name: "offset decimal", raw: "id=7&option=10&ofssec=1.5", reason: "invalid-offset"},
		{name: "offset exponent", raw: "id=7&option=10&ofssec=1e2", reason: "invalid-offset"},
		{name: "offset leading zero", raw: "id=7&option=10&ofssec=01", reason: "invalid-offset"},
		{name: "offset overflow", raw: "id=7&option=10&ofssec=18446744073709551616", reason: "invalid-offset"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, reason := parseXcodeQuery(test.raw)
			if id != test.wantID || reason != test.reason {
				t.Fatalf("id=%d reason=%q want_id=%d want_reason=%q", id, reason, test.wantID, test.reason)
			}
		})
	}
}

func TestXcodeReturnsOriginalFileWithHeadAndSingleRanges(t *testing.T) {
	data := make([]byte, 188*4)
	for index := range data {
		data[index] = byte(index)
	}
	files := &fakeFiles{data: data}
	handler, err := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItemWithBytes(7, int64(len(data)))}}, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/xcode?id=7&option=10",
		"/api/xcode?fname=recordings%2F7.ts&option=10&ctok=",
		"/api/xcode?id=7&option=10&ofssec=3600",
	} {
		response := serve(handler, http.MethodGet, path, nil)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), data) ||
			response.Header().Get("Content-Type") != "video/mp2t" || response.Header().Get("Content-Length") != strconv.Itoa(len(data)) ||
			response.Header().Get("Accept-Ranges") != "bytes" {
			t.Fatalf("path=%q code=%d header=%v bytes=%d", path, response.Code, response.Header(), response.Body.Len())
		}
	}
	head := serve(handler, http.MethodHead, "/api/xcode?id=7&option=10", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(data)) ||
		head.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("head=%d header=%v bytes=%d", head.Code, head.Header(), head.Body.Len())
	}
	headRange := serve(handler, http.MethodHead, "/api/xcode?id=7&option=10", map[string]string{"Range": "bytes=188-375"})
	if headRange.Code != http.StatusPartialContent || headRange.Body.Len() != 0 ||
		headRange.Header().Get("Content-Length") != "188" || headRange.Header().Get("Content-Range") != "bytes 188-375/752" {
		t.Fatalf("head range=%d header=%v bytes=%d", headRange.Code, headRange.Header(), headRange.Body.Len())
	}
	ranges := []struct {
		header       string
		contentRange string
		want         []byte
	}{
		{header: "bytes=0-0", contentRange: "bytes 0-0/752", want: data[:1]},
		{header: "bytes=188-375", contentRange: "bytes 188-375/752", want: data[188:376]},
		{header: "bytes=564-751", contentRange: "bytes 564-751/752", want: data[564:]},
		{header: "bytes=-1", contentRange: "bytes 751-751/752", want: data[len(data)-1:]},
	}
	for _, test := range ranges {
		response := serve(handler, http.MethodGet, "/api/xcode?fname=recordings%2F7.ts&option=10", map[string]string{"Range": test.header})
		if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), test.want) ||
			response.Header().Get("Content-Range") != test.contentRange || response.Header().Get("Content-Length") != strconv.Itoa(len(test.want)) {
			t.Fatalf("range=%q code=%d header=%v bytes=%d", test.header, response.Code, response.Header(), response.Body.Len())
		}
	}
	if response := serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", map[string]string{"Range": "bytes=752-"}); response.Code != http.StatusRequestedRangeNotSatisfiable || response.Header().Get("Content-Range") != "bytes */752" {
		t.Fatalf("outside=%d header=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	if response := serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", map[string]string{"Range": "bytes=0-1,4-5"}); response.Code != http.StatusRequestedRangeNotSatisfiable || response.Body.String() != "multiple-ranges-unsupported\n" {
		t.Fatalf("multiple=%d body=%q", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/xcode?id=7&option=10", nil)
	request.Header.Add("Range", "bytes=0-1")
	request.Header.Add("Range", "bytes=4-5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Body.String() != "multiple-ranges-unsupported\n" {
		t.Fatalf("repeated range=%d body=%q", response.Code, response.Body.String())
	}
	method := serve(handler, http.MethodPost, "/api/xcode?id=7&option=10", nil)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD" || files.opened.Load() != files.closed.Load() {
		t.Fatalf("method=%d header=%v opened=%d closed=%d", method.Code, method.Header(), files.opened.Load(), files.closed.Load())
	}
	for _, path := range []string{"/api/Xcode?id=7&option=10", "/api/xcode/?id=7&option=10", "/api/xcode/extra?id=7&option=10"} {
		response := serve(handler, http.MethodGet, path, nil)
		if response.Code != http.StatusNotFound || response.Code >= 300 && response.Code < 400 {
			t.Fatalf("path=%q code=%d", path, response.Code)
		}
	}
}

func TestXcodeValidatesEveryQueryBeforeHistoryOrFile(t *testing.T) {
	history := &fakeHistory{err: errors.New("private history")}
	files := &fakeFiles{err: errors.New("private file")}
	handler, _ := NewHandler(history, files)
	tests := []struct {
		raw    string
		reason string
	}{
		{raw: "id=7&id=8&option=10", reason: "invalid-query"},
		{raw: "id=7&option=10&option=10", reason: "unsupported-option"},
		{raw: "id=7&option=10&ctok=&ctok=", reason: "invalid-token"},
		{raw: "id=7&option=10&ofssec=1&ofssec=2", reason: "invalid-offset"},
		{raw: "id=7;option=10", reason: "invalid-query"},
		{raw: "id=7&option=10&ctok=%ZZ", reason: "invalid-query"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "/api/xcode", nil)
		request.URL.RawQuery = test.raw
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Body.String() != test.reason+"\n" {
			t.Fatalf("raw=%q code=%d body=%q", test.raw, response.Code, response.Body.String())
		}
	}
	if history.calls.Load() != 0 || files.opened.Load() != 0 {
		t.Fatalf("history=%d opened=%d", history.calls.Load(), files.opened.Load())
	}
}

func TestXcodeSharesPlayableConditions(t *testing.T) {
	partial := httpHistoryItem(7)
	partial.State = recording.AttemptPartial
	partial.Reason = recording.ReasonUserRequestedStop
	for _, path := range []string{"/recordings/7.ts", "/api/xcode?id=7&option=10"} {
		handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{partial}}, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})
		if response := serve(handler, http.MethodGet, path, nil); response.Code != http.StatusOK {
			t.Fatalf("safe partial path=%q code=%d body=%q", path, response.Code, response.Body.String())
		}
	}

	changes := []struct {
		name   string
		change func(*recording.HistoryItem)
	}{
		{name: "recording", change: func(item *recording.HistoryItem) { item.State = recording.AttemptRecording }},
		{name: "finalizing", change: func(item *recording.HistoryItem) { item.State = recording.AttemptFinalizing }},
		{name: "other partial", change: func(item *recording.HistoryItem) {
			item.State, item.Reason = recording.AttemptPartial, recording.ReasonStreamEndedEarly
		}},
		{name: "failed", change: func(item *recording.HistoryItem) {
			item.State, item.Reason = recording.AttemptFailed, recording.ReasonStreamUnavailable
		}},
		{name: "actual time missing", change: func(item *recording.HistoryItem) { item.ActualStart, item.ActualEnd = nil, nil }},
		{name: "short", change: func(item *recording.HistoryItem) { item.ByteCount = 187 }},
		{name: "segment", change: func(item *recording.HistoryItem) { item.SegmentState = recording.SegmentPartial }},
		{name: "availability", change: func(item *recording.HistoryItem) { item.Availability = recording.AvailabilityPartial }},
		{name: "file sync", change: func(item *recording.HistoryItem) { item.FileSynced = false }},
		{name: "publication", change: func(item *recording.HistoryItem) { item.FinalPublished = false }},
		{name: "directory sync", change: func(item *recording.HistoryItem) { item.DirectorySynced = false }},
		{name: "file plan", change: func(item *recording.HistoryItem) { item.Plan = recording.FilePlan{} }},
	}
	for _, test := range changes {
		t.Run(test.name, func(t *testing.T) {
			item := httpHistoryItem(7)
			test.change(&item)
			for _, path := range []string{"/recordings/7.ts", "/api/xcode?id=7&option=10"} {
				files := &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)}
				handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{item}}, files)
				if response := serve(handler, http.MethodGet, path, nil); response.Code != http.StatusNotFound || files.opened.Load() != 0 {
					t.Fatalf("path=%q code=%d opened=%d body=%q", path, response.Code, files.opened.Load(), response.Body.String())
				}
			}
		})
	}
	handler, _ := NewHandler(&fakeHistory{}, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})
	if response := serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", nil); response.Code != http.StatusNotFound {
		t.Fatalf("empty history=%d body=%q", response.Code, response.Body.String())
	}
}

func TestXcodeAndDirectURLShareStreamLimitAndCleanup(t *testing.T) {
	files := &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)}
	handler, err := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, files)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, maximumStreams)
	done := make(chan struct{}, maximumStreams)
	for index := range maximumStreams {
		path := "/recordings/7.ts"
		if index%2 == 1 {
			path = "/api/xcode?id=7&option=10"
		}
		go func() {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			writer := &blockingResponseWriter{header: make(http.Header), started: started, release: release}
			handler.ServeHTTP(writer, request)
			done <- struct{}{}
		}()
	}
	for range maximumStreams {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("shared stream did not start")
		}
	}
	for _, path := range []string{"/recordings/7.ts", "/api/xcode?id=7&option=10"} {
		response := serve(handler, http.MethodGet, path, nil)
		if response.Code != http.StatusServiceUnavailable || response.Body.String() != "stream-limit\n" {
			t.Fatalf("path=%q code=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	close(release)
	for range maximumStreams {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("shared stream did not stop")
		}
	}
	if response := serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", nil); response.Code != http.StatusOK {
		t.Fatalf("reused=%d body=%q", response.Code, response.Body.String())
	}
	if files.opened.Load() != maximumStreams+1 || files.closed.Load() != maximumStreams+1 || len(handler.streams) != 0 {
		t.Fatalf("opened=%d closed=%d slots=%d", files.opened.Load(), files.closed.Load(), len(handler.streams))
	}
}

func TestXcodeReleasesResourcesAfterCancelAndFailures(t *testing.T) {
	files := &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)}
	handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, files)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/xcode?id=7&option=10", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || files.opened.Load() != 0 || len(handler.streams) != 0 {
		t.Fatalf("cancel=%d opened=%d slots=%d", response.Code, files.opened.Load(), len(handler.streams))
	}
	request = httptest.NewRequest(http.MethodGet, "/api/xcode?id=7&option=10", nil)
	handler.ServeHTTP(&failingResponseWriter{header: make(http.Header)}, request)
	if files.opened.Load() != 1 || files.closed.Load() != 1 || len(handler.streams) != 0 {
		t.Fatalf("write failure opened=%d closed=%d slots=%d", files.opened.Load(), files.closed.Load(), len(handler.streams))
	}

	fileFailure := &fakeFiles{err: errors.New("private file")}
	handler, _ = NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, fileFailure)
	if response := serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", nil); response.Code != http.StatusNotFound || response.Body.String() != "file-unavailable\n" || len(handler.streams) != 0 {
		t.Fatalf("file failure=%d body=%q slots=%d", response.Code, response.Body.String(), len(handler.streams))
	}
	handler, _ = NewHandler(&fakeHistory{err: errors.New("private history")}, &fakeFiles{})
	if response := serve(handler, http.MethodGet, "/api/xcode?id=7&option=10", nil); response.Code != http.StatusServiceUnavailable ||
		response.Body.String() != "history-unavailable\n" || len(handler.streams) != 0 {
		t.Fatalf("history failure=%d body=%q slots=%d", response.Code, response.Body.String(), len(handler.streams))
	}
}

func TestXcodeStopsReadingAfterRequestCancellation(t *testing.T) {
	data := bytes.Repeat([]byte{0x47}, 188*1000)
	files := &fakeFiles{data: data}
	handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItemWithBytes(7, int64(len(data)))}}, files)
	for _, rangeHeader := range []string{"", "bytes=188-"} {
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodGet, "/api/xcode?id=7&option=10", nil).WithContext(ctx)
		if rangeHeader != "" {
			request.Header.Set("Range", rangeHeader)
		}
		writer := &cancelingResponseWriter{header: make(http.Header), cancel: cancel}
		handler.ServeHTTP(writer, request)
		cancel()
		if writer.writes != 1 || len(handler.streams) != 0 {
			t.Fatalf("range=%q writes=%d slots=%d", rangeHeader, writer.writes, len(handler.streams))
		}
	}
	if files.opened.Load() != 2 || files.closed.Load() != 2 {
		t.Fatalf("opened=%d closed=%d", files.opened.Load(), files.closed.Load())
	}
}

func TestResolverOptionBuildsXcodeRangeRequest(t *testing.T) {
	data := bytes.Repeat([]byte{0x47}, 188*2)
	handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItemWithBytes(7, int64(len(data)))}}, &fakeFiles{data: data})
	settings := serve(handler, http.MethodGet, "/komorebi/resolver.lua", nil)
	var options struct {
		Options []struct {
			ID string `json:"id"`
		} `json:"option"`
	}
	if err := json.Unmarshal(settings.Body.Bytes(), &options); err != nil || len(options.Options) != 1 {
		t.Fatalf("settings=%s err=%v", settings.Body.String(), err)
	}
	resolved := serve(handler, http.MethodGet, "/komorebi/resolver.lua?id=7", nil)
	var urls map[string]string
	if err := json.Unmarshal(resolved.Body.Bytes(), &urls); err != nil {
		t.Fatal(err)
	}
	query := url.Values{
		"fname":  {strings.TrimPrefix(urls["video_url"], "/")},
		"option": {options.Options[0].ID},
		"ctok":   {""},
	}
	response := serve(handler, http.MethodGet, "/api/xcode?"+query.Encode(), map[string]string{"Range": "bytes=0-187"})
	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), data[:188]) {
		t.Fatalf("code=%d body=%d", response.Code, response.Body.Len())
	}
	idResponse := serve(handler, http.MethodGet, "/api/xcode?id=7&option="+url.QueryEscape(options.Options[0].ID), map[string]string{"Range": "bytes=188-375"})
	if idResponse.Code != http.StatusPartialContent || !bytes.Equal(idResponse.Body.Bytes(), data[188:]) {
		t.Fatalf("id code=%d body=%d", idResponse.Code, idResponse.Body.Len())
	}
}

type cancelingResponseWriter struct {
	header http.Header
	cancel context.CancelFunc
	writes int
}

func (writer *cancelingResponseWriter) Header() http.Header { return writer.header }
func (*cancelingResponseWriter) WriteHeader(int)            {}
func (writer *cancelingResponseWriter) Write(data []byte) (int, error) {
	writer.writes++
	writer.cancel()
	return len(data), nil
}
