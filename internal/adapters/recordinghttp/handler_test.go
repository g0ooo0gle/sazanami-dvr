package recordinghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

var logoPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}

func TestNativeHistoryResolverAndRelatedAssets(t *testing.T) {
	item := httpHistoryItem(7)
	history := &fakeHistory{items: []recording.HistoryItem{item}}
	handler, err := NewHandler(history, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})
	if err != nil {
		t.Fatal(err)
	}

	response := serve(handler, http.MethodGet, "/api/recordings?limit=1", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("code=%d header=%v", response.Code, response.Header())
	}
	var list listResponse
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Recordings) != 1 || list.Recordings[0].ID != 7 || !list.Recordings[0].Playable || list.Recordings[0].PlaybackURL == nil || list.NextBefore == nil {
		t.Fatalf("list=%+v", list)
	}

	response = serve(handler, http.MethodGet, "/api/recordings/7", nil)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	response = serve(handler, http.MethodHead, "/api/recordings/7", nil)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("detail head=%d body=%q", response.Code, response.Body.String())
	}
	response = serve(handler, http.MethodHead, "/api/recordings?limit=1", nil)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("list head=%d body=%q", response.Code, response.Body.String())
	}
	response = serve(handler, http.MethodGet, "/komorebi/resolver.lua?id=7", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"video_url":"/recordings/7.ts"`)) {
		t.Fatalf("resolver=%d %s", response.Code, response.Body.String())
	}
	response = serve(handler, http.MethodGet, "/api/Thumbnail?id=7", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Body.Len() == 0 {
		t.Fatalf("thumbnail=%d bytes=%d", response.Code, response.Body.Len())
	}
	response = serve(handler, http.MethodGet, "/komorebi/chapters/7", nil)
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("chapters=%d %q", response.Code, response.Body.String())
	}
	response = serve(handler, http.MethodGet, "/komorebi/tiles/7.json", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"total_tiles":1`)) {
		t.Fatalf("tiles=%d %s", response.Code, response.Body.String())
	}
}

func TestKomorebiLogoReturnsBoundedPNG(t *testing.T) {
	catalog := &fakeLogoCatalog{target: "100002"}
	logos := &fakeLogoProvider{data: logoPNG}
	handler, err := NewHandlerWithLogos(&fakeHistory{}, &fakeFiles{}, catalog, logos)
	if err != nil {
		t.Fatal(err)
	}
	response := serve(handler, http.MethodGet, "/legacy/logo.lua?onid=1&sid=2", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" ||
		response.Header().Get("Content-Length") != "11" || !bytes.Equal(response.Body.Bytes(), logoPNG) {
		t.Fatalf("code=%d header=%v body=%x", response.Code, response.Header(), response.Body.Bytes())
	}
	if catalog.calls.Load() != 1 || catalog.network != 1 || catalog.service != 2 || logos.calls.Load() != 1 || logos.target != "100002" {
		t.Fatalf("catalog=%d/%d calls=%d logo target=%q calls=%d", catalog.network, catalog.service,
			catalog.calls.Load(), logos.target, logos.calls.Load())
	}
	response = serve(handler, http.MethodHead, "/legacy/logo.lua?onid=1&sid=2", nil)
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "11" {
		t.Fatalf("head=%d header=%v body=%x", response.Code, response.Header(), response.Body.Bytes())
	}
}

func TestKomorebiLogoRejectsInvalidOrUnavailableRequests(t *testing.T) {
	validPath := "/legacy/logo.lua?onid=1&sid=2"
	invalid := []string{
		"/legacy/logo.lua", "/legacy/logo.lua?onid=1", "/legacy/logo.lua?onid=1&sid=2&x=3",
		"/legacy/logo.lua?onid=1&onid=2&sid=2", "/legacy/logo.lua?onid=01&sid=2",
		"/legacy/logo.lua?onid=0&sid=2", "/legacy/logo.lua?onid=65536&sid=2",
		"/legacy/logo.lua?onid=1&sid=-1",
	}
	for _, path := range invalid {
		handler, _ := NewHandlerWithLogos(&fakeHistory{}, &fakeFiles{}, &fakeLogoCatalog{target: "100002"}, &fakeLogoProvider{data: logoPNG})
		if response := serve(handler, http.MethodGet, path, nil); response.Code != http.StatusBadRequest {
			t.Fatalf("path=%q code=%d body=%q", path, response.Code, response.Body.String())
		}
	}

	cases := []struct {
		name    string
		catalog *fakeLogoCatalog
		logos   *fakeLogoProvider
		status  int
	}{
		{name: "unknown", catalog: &fakeLogoCatalog{err: errors.New("private catalog")}, logos: &fakeLogoProvider{data: logoPNG}, status: http.StatusNotFound},
		{name: "missing", catalog: &fakeLogoCatalog{target: "100002"}, logos: &fakeLogoProvider{err: provider.NewFailure(provider.ReasonNotFound, "private provider")}, status: http.StatusNotFound},
		{name: "failure", catalog: &fakeLogoCatalog{target: "100002"}, logos: &fakeLogoProvider{err: errors.New("private provider")}, status: http.StatusServiceUnavailable},
		{name: "invalid", catalog: &fakeLogoCatalog{target: "100002"}, logos: &fakeLogoProvider{data: []byte("not png")}, status: http.StatusServiceUnavailable},
		{name: "oversize", catalog: &fakeLogoCatalog{target: "100002"}, logos: &fakeLogoProvider{data: append(append([]byte(nil), logoPNG...), make([]byte, maximumLogo)...)}, status: http.StatusServiceUnavailable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := NewHandlerWithLogos(&fakeHistory{}, &fakeFiles{}, test.catalog, test.logos)
			response := serve(handler, http.MethodGet, validPath, nil)
			if response.Code != test.status || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("code=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
	oldHandler, _ := NewHandler(&fakeHistory{}, &fakeFiles{})
	if response := serve(oldHandler, http.MethodGet, validPath, nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("dependency code=%d", response.Code)
	}
	handler, _ := NewHandlerWithLogos(&fakeHistory{}, &fakeFiles{}, &fakeLogoCatalog{target: "100002"}, &fakeLogoProvider{data: logoPNG})
	if response := serve(handler, http.MethodPost, validPath, nil); response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("method code=%d header=%v", response.Code, response.Header())
	}
}

func TestRecordingStreamSupportsHeadAndSingleRange(t *testing.T) {
	data := make([]byte, 188*2)
	for index := range data {
		data[index] = byte(index)
	}
	handler, err := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItemWithBytes(7, int64(len(data)))}}, &fakeFiles{data: data})
	if err != nil {
		t.Fatal(err)
	}
	response := serve(handler, http.MethodGet, "/recordings/7.ts", nil)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), data) || response.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("code=%d len=%d headers=%v", response.Code, response.Body.Len(), response.Header())
	}
	response = serve(handler, http.MethodHead, "/recordings/7.ts", nil)
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "376" {
		t.Fatalf("head=%d len=%d headers=%v", response.Code, response.Body.Len(), response.Header())
	}
	response = serve(handler, http.MethodGet, "/recordings/7.ts", map[string]string{"Range": "bytes=188-375"})
	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), data[188:]) || response.Header().Get("Content-Range") != "bytes 188-375/376" {
		t.Fatalf("range=%d len=%d headers=%v", response.Code, response.Body.Len(), response.Header())
	}
	response = serve(handler, http.MethodGet, "/recordings/7.ts", map[string]string{"Range": "bytes=0-1,4-5"})
	if response.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatal(response.Code)
	}
	response = serve(handler, http.MethodGet, "/recordings/7.ts", map[string]string{"Range": "bytes=376-"})
	if response.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatal(response.Code)
	}
	if files := handler.files.(*fakeFiles); files.opened.Load() != files.closed.Load() {
		t.Fatalf("opened=%d closed=%d", files.opened.Load(), files.closed.Load())
	}
}

func TestUserStoppedPartialRecordingSupportsNativeDetailAndRange(t *testing.T) {
	data := bytes.Repeat([]byte{0x47}, 188)
	item := httpHistoryItemWithBytes(8, int64(len(data)))
	item.State = recording.AttemptPartial
	item.Reason = recording.ReasonUserRequestedStop
	handler, err := NewHandler(&fakeHistory{items: []recording.HistoryItem{item}}, &fakeFiles{data: data})
	if err != nil {
		t.Fatal(err)
	}
	detail := serve(handler, http.MethodGet, "/api/recordings/8", nil)
	if detail.Code != http.StatusOK || !bytes.Contains(detail.Body.Bytes(), []byte(`"state":"PARTIAL"`)) ||
		!bytes.Contains(detail.Body.Bytes(), []byte(`"reason":"USER_REQUESTED_STOP"`)) ||
		!bytes.Contains(detail.Body.Bytes(), []byte(`"playable":true`)) {
		t.Fatalf("detail=%d body=%s", detail.Code, detail.Body.String())
	}
	ranged := serve(handler, http.MethodGet, "/recordings/8.ts", map[string]string{"Range": "bytes=0-187"})
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), data) {
		t.Fatalf("range=%d bytes=%d", ranged.Code, ranged.Body.Len())
	}
	item.Reason = recording.ReasonStreamEndedEarly
	handler, _ = NewHandler(&fakeHistory{items: []recording.HistoryItem{item}}, &fakeFiles{data: data})
	if response := serve(handler, http.MethodGet, "/recordings/8.ts", nil); response.Code != http.StatusNotFound {
		t.Fatalf("利用者停止以外の部分録画 code=%d", response.Code)
	}
}

func TestRecordingStreamLimitsConcurrentReadersAndClosesFiles(t *testing.T) {
	files := &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)}
	handler, err := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, files)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{}, maximumStreams)
	done := make(chan struct{}, maximumStreams)
	for range maximumStreams {
		go func() {
			request := httptest.NewRequest(http.MethodGet, "/recordings/7.ts", nil)
			writer := &blockingResponseWriter{header: make(http.Header), started: started, release: release}
			handler.ServeHTTP(writer, request)
			done <- struct{}{}
		}()
	}
	for range maximumStreams {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("stream did not start")
		}
	}
	ninth := serve(handler, http.MethodGet, "/recordings/7.ts", nil)
	if ninth.Code != http.StatusServiceUnavailable || ninth.Body.String() != "stream-limit\n" {
		t.Fatalf("ninth=%d body=%q", ninth.Code, ninth.Body.String())
	}
	close(release)
	for range maximumStreams {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("stream did not stop")
		}
	}
	if files.opened.Load() != maximumStreams || files.closed.Load() != maximumStreams {
		t.Fatalf("opened=%d closed=%d", files.opened.Load(), files.closed.Load())
	}
}

func TestHTTPRedactsPathsAndSourceErrors(t *testing.T) {
	item := httpHistoryItem(7)
	item.Title = "<番組>& /private/title"
	item.Plan = recording.FilePlan{PartialPath: "2026/08/private.ts.partial", FinalPath: "2026/08/private.ts"}
	handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{item}}, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})
	response := serve(handler, http.MethodGet, "/api/recordings/7", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`\u003c番組\u003e\u0026`)) ||
		bytes.Contains(response.Body.Bytes(), []byte("private.ts")) {
		t.Fatalf("code=%d body=%q", response.Code, response.Body.String())
	}
	handler, _ = NewHandler(&fakeHistory{err: errors.New("/private/database")}, &fakeFiles{})
	response = serve(handler, http.MethodGet, "/api/recordings", nil)
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "history-unavailable\n" ||
		strings.Contains(response.Body.String(), "private") {
		t.Fatalf("code=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRecordingStreamStopsOnCancelOrClientDisconnect(t *testing.T) {
	files := &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)}
	handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{httpHistoryItem(7)}}, files)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/recordings/7.ts", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || files.opened.Load() != 0 {
		t.Fatalf("cancel code=%d opened=%d", response.Code, files.opened.Load())
	}
	request = httptest.NewRequest(http.MethodGet, "/recordings/7.ts", nil)
	handler.ServeHTTP(&failingResponseWriter{header: make(http.Header)}, request)
	if files.opened.Load() != 1 || files.closed.Load() != 1 {
		t.Fatalf("disconnect opened=%d closed=%d", files.opened.Load(), files.closed.Load())
	}
}

func TestHTTPRejectsUnknownQueriesMethodsAndUnavailableFiles(t *testing.T) {
	item := httpHistoryItem(7)
	item.State = recording.AttemptFailed
	item.Reason = recording.ReasonStreamUnavailable
	handler, _ := NewHandler(&fakeHistory{items: []recording.HistoryItem{item}}, &fakeFiles{data: bytes.Repeat([]byte{0x47}, 188)})
	cases := []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/api/recordings", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/recordings?limit=1&limit=2", http.StatusBadRequest},
		{http.MethodGet, "/api/recordings?limit=0", http.StatusBadRequest},
		{http.MethodGet, "/api/recordings?limit=101", http.StatusBadRequest},
		{http.MethodGet, "/api/recordings?before=0", http.StatusBadRequest},
		{http.MethodGet, "/api/recordings?before=2147483648", http.StatusBadRequest},
		{http.MethodGet, "/api/recordings?unknown=1", http.StatusBadRequest},
		{http.MethodGet, "/api/recordings/2147483648", http.StatusNotFound},
		{http.MethodGet, "/komorebi/resolver.lua?id=7", http.StatusNotFound},
		{http.MethodGet, "/recordings/7.ts", http.StatusNotFound},
		{http.MethodGet, "/private/path", http.StatusNotFound},
	}
	for _, test := range cases {
		response := serve(handler, test.method, test.path, nil)
		if response.Code != test.status {
			t.Fatalf("%s %s code=%d want=%d body=%q", test.method, test.path, response.Code, test.status, response.Body.String())
		}
	}
}

func TestValidateListenAddressAllowsOnlyExplicitLocalIP(t *testing.T) {
	for _, address := range []string{"127.0.0.1:40773", "[::1]:40773", "10.0.0.8:40773", "192.168.1.8:40773", "[fd00::8]:40773", "0.0.0.0:40773", "[::]:40773"} {
		if err := ValidateListenAddress(address, false); err != nil {
			t.Fatalf("address=%s err=%v", address, err)
		}
	}
	for _, address := range []string{"localhost:40773", "8.8.8.8:40773", "169.254.1.1:40773", "127.0.0.1:0"} {
		if err := ValidateListenAddress(address, false); err == nil {
			t.Fatalf("address=%s accepted", address)
		}
	}
}

type fakeHistory struct {
	items []recording.HistoryItem
	err   error
	calls atomic.Int32
}

type fakeLogoCatalog struct {
	target  string
	err     error
	network uint16
	service uint16
	calls   atomic.Int32
}

func (catalog *fakeLogoCatalog) ResolveLogoService(_ context.Context, networkID, serviceID uint16) (provider.TuningTarget, error) {
	catalog.calls.Add(1)
	catalog.network = networkID
	catalog.service = serviceID
	if catalog.err != nil {
		return provider.TuningTarget{}, catalog.err
	}
	return provider.NewTuningTarget(catalog.target)
}

type fakeLogoProvider struct {
	data   []byte
	err    error
	target string
	calls  atomic.Int32
}

func (logos *fakeLogoProvider) Logo(_ context.Context, target provider.TuningTarget) ([]byte, error) {
	logos.calls.Add(1)
	logos.target = target.Opaque
	return logos.data, logos.err
}

func (history *fakeHistory) RecordingHistory(_ context.Context, limit int, before int32) ([]recording.HistoryItem, error) {
	if history.err != nil {
		return nil, history.err
	}
	result := make([]recording.HistoryItem, 0, limit)
	for _, item := range history.items {
		if before == 0 || item.Number < before {
			result = append(result, item)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
func (history *fakeHistory) RecordingHistoryItem(_ context.Context, id int32) (*recording.HistoryItem, error) {
	history.calls.Add(1)
	if history.err != nil {
		return nil, history.err
	}
	for index := range history.items {
		if history.items[index].Number == id {
			return &history.items[index], nil
		}
	}
	return nil, nil
}

type fakeFiles struct {
	data   []byte
	err    error
	opened atomic.Int32
	closed atomic.Int32
}

func (files *fakeFiles) OpenFinal(_ recording.FilePlan, size int64) (FinalFile, error) {
	if files.err != nil {
		return nil, files.err
	}
	if int64(len(files.data)) != size {
		return nil, io.ErrUnexpectedEOF
	}
	files.opened.Add(1)
	return &memoryFinal{Reader: *bytes.NewReader(files.data), owner: files}, nil
}

type memoryFinal struct {
	bytes.Reader
	owner *fakeFiles
}

func (file *memoryFinal) Close() error       { file.owner.closed.Add(1); return nil }
func (file *memoryFinal) ModTime() time.Time { return time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC) }

type blockingResponseWriter struct {
	header  http.Header
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (writer *blockingResponseWriter) Header() http.Header { return writer.header }
func (*blockingResponseWriter) WriteHeader(int)            {}
func (writer *blockingResponseWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { writer.started <- struct{}{} })
	<-writer.release
	return len(data), nil
}

type failingResponseWriter struct{ header http.Header }

func (writer *failingResponseWriter) Header() http.Header { return writer.header }
func (*failingResponseWriter) WriteHeader(int)            {}
func (*failingResponseWriter) Write([]byte) (int, error)  { return 0, io.ErrClosedPipe }

func httpHistoryItem(number int32) recording.HistoryItem {
	return httpHistoryItemWithBytes(number, 188)
}
func httpHistoryItemWithBytes(number int32, size int64) recording.HistoryItem {
	start := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	return recording.HistoryItem{Number: number, State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted, Title: "番組", StationName: "放送局", NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4, PlannedStart: start, PlannedEnd: end, ActualStart: &start, ActualEnd: &end, ByteCount: size, Plan: recording.FilePlan{PartialPath: "2026/08/x.ts.partial", FinalPath: "2026/08/x.ts"}, SegmentState: recording.SegmentFinalized, Availability: recording.AvailabilityFinal, FileSynced: true, FinalPublished: true, DirectorySynced: true}
}
func serve(handler http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
