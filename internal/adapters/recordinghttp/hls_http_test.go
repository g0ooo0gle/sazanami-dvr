package recordinghttp

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	validHLSTvCastQuery = "id=1-2-3&n=0&json=1&ctok="
	validHLSViewQuery   = "id=1-2-3&n=0&option=10&hls=A_1-test&ctok="
	validHLSViewBody    = "ctok=&open=1"
)

func TestParseHLSTvCastRequestAcceptsOnlyCanonicalInput(t *testing.T) {
	request := newHLSRequest(http.MethodGet, "/api/TvCast", validHLSTvCastQuery, "")
	got, rejection := parseHLSTvCastRequest(request)
	want := hlsTvCastRequest{service: hlsServiceID{onid: 1, tsid: 2, sid: 3}, slot: 0}
	if rejection != nil || got != want {
		t.Fatalf("request=%+v rejection=%+v want=%+v", got, rejection, want)
	}

	maximum := newHLSRequest(http.MethodGet, "/api/TvCast", "id=65535-65535-65535&n=1&json=1&ctok=", "")
	got, rejection = parseHLSTvCastRequest(maximum)
	want = hlsTvCastRequest{service: hlsServiceID{onid: math.MaxUint16, tsid: math.MaxUint16, sid: math.MaxUint16}, slot: 1}
	if rejection != nil || got != want {
		t.Fatalf("maximum=%+v rejection=%+v want=%+v", got, rejection, want)
	}

	invalid := []string{
		"",
		"id=1-2-3&n=0&json=1&ctok=&unknown=1",
		"i%64=1-2-3&n=0&json=1&ctok=",
		"ID=1-2-3&n=0&json=1&ctok=",
		"id=1-2-3&N=0&json=1&ctok=",
		"id=1-2-3&n=0&json=1&ctok=%ZZ",
		"id=1-2-3;n=0&json=1&ctok=",
		"id=1-2-3&n=0&json=1&ctok=&",
		"id=1-2-3&&n=0&json=1&ctok=",
		"=1&id=1-2-3&n=0&json=1&ctok=",
		"id=0-2-3&n=0&json=1&ctok=",
		"id=01-2-3&n=0&json=1&ctok=",
		"id=%2B1-2-3&n=0&json=1&ctok=",
		"id=-1-2-3&n=0&json=1&ctok=",
		"id=1--3&n=0&json=1&ctok=",
		"id=1-2&n=0&json=1&ctok=",
		"id=1-2-3-4&n=0&json=1&ctok=",
		"id=65536-2-3&n=0&json=1&ctok=",
		"id=18446744073709551616-2-3&n=0&json=1&ctok=",
		"id=1-2-3x&n=0&json=1&ctok=",
		"id=1-2-3&n=2&json=1&ctok=",
		"id=1-2-3&n=00&json=1&ctok=",
		"id=1-2-3&n=%2B0&json=1&ctok=",
		"id=1-2-3&n=0&json=0&ctok=",
		"id=1-2-3&n=0&json=01&ctok=",
		"id=1-2-3&n=0&json=1&ctok=token",
	}
	invalid = append(invalid, missingAndDuplicateHLSQueries([]string{
		"id=1-2-3", "n=0", "json=1", "ctok=",
	})...)
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			_, failure := parseHLSTvCastRequest(newHLSRequest(http.MethodGet, "/api/TvCast", raw, ""))
			assertHLSRejection(t, failure, http.StatusBadRequest, "invalid-query", "")
		})
	}
}

func TestParseHLSTvCastRequestReportsMethodAndPath(t *testing.T) {
	_, rejection := parseHLSTvCastRequest(newHLSRequest(http.MethodPost, "/api/TvCast", validHLSTvCastQuery, ""))
	assertHLSRejection(t, rejection, http.StatusMethodNotAllowed, "method-not-allowed", http.MethodGet)

	for _, path := range []string{"/api/tvcast", "/api/TvCast/", "/api/TvCast.json"} {
		_, rejection = parseHLSTvCastRequest(newHLSRequest(http.MethodGet, path, validHLSTvCastQuery, ""))
		assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-query", "")
	}
	encoded := newHLSRequest(http.MethodGet, "/api/TvCast", validHLSTvCastQuery, "")
	encoded.URL.RawPath = "/api/%54vCast"
	_, rejection = parseHLSTvCastRequest(encoded)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-query", "")
}

func TestParseHLSViewRequestAcceptsGETAndPOST(t *testing.T) {
	post := newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, validHLSViewBody)
	got, rejection := parseHLSViewRequest(post)
	want := hlsViewRequest{
		service: hlsServiceID{onid: 1, tsid: 2, sid: 3}, slot: 0, key: "A_1-test", operation: hlsViewStart,
	}
	if rejection != nil || got != want {
		t.Fatalf("post=%+v rejection=%+v want=%+v", got, rejection, want)
	}

	get := newValidHLSViewRequest(http.MethodGet, "id=65535-2-3&n=1&option=10&hls=Z&ctok=", "")
	got, rejection = parseHLSViewRequest(get)
	want = hlsViewRequest{
		service: hlsServiceID{onid: math.MaxUint16, tsid: 2, sid: 3}, slot: 1, key: "Z", operation: hlsViewPlaylist,
	}
	if rejection != nil || got != want {
		t.Fatalf("get=%+v rejection=%+v want=%+v", got, rejection, want)
	}

	maximumKey := "A" + strings.Repeat("z", 95)
	maximum := newValidHLSViewRequest(http.MethodGet, "id=1-2-3&n=0&option=10&hls="+maximumKey+"&ctok=", "")
	got, rejection = parseHLSViewRequest(maximum)
	if rejection != nil || got.key != maximumKey {
		t.Fatalf("maximum key bytes=%d got=%+v rejection=%+v", len(maximumKey), got, rejection)
	}
}

func TestParseHLSViewRequestRejectsInvalidQuery(t *testing.T) {
	invalid := []string{
		"",
		"id=1-2-3&n=0&option=10&hls=A_1-test&ctok=&unknown=1",
		"id=1-2-3&n=0&option=10&h%6cs=A&ctok=",
		"ID=1-2-3&n=0&option=10&hls=A&ctok=",
		"id=1-2-3&n=0&Option=10&hls=A&ctok=",
		"id=1-2-3&n=0&option=10&hls=A&ctok=%ZZ",
		"id=1-2-3;n=0&option=10&hls=A&ctok=",
		"id=1-2-3&n=0&option=10&hls=A&ctok=&",
		"id=01-2-3&n=0&option=10&hls=A&ctok=",
		"id=65536-2-3&n=0&option=10&hls=A&ctok=",
		"id=1-2-3x&n=0&option=10&hls=A&ctok=",
		"id=1-2-3&n=2&option=10&hls=A&ctok=",
		"id=1-2-3&n=0&option=010&hls=A&ctok=",
		"id=1-2-3&n=0&option=11&hls=A&ctok=",
		"id=1-2-3&n=0&option=10&hls=&ctok=",
		"id=1-2-3&n=0&option=10&hls=_A&ctok=",
		"id=1-2-3&n=0&option=10&hls=-A&ctok=",
		"id=1-2-3&n=0&option=10&hls=A.B&ctok=",
		"id=1-2-3&n=0&option=10&hls=%E3%81%82&ctok=",
		"id=1-2-3&n=0&option=10&hls=" + "A" + strings.Repeat("z", 96) + "&ctok=",
		"id=1-2-3&n=0&option=10&hls=A&ctok=token",
	}
	invalid = append(invalid, missingAndDuplicateHLSQueries([]string{
		"id=1-2-3", "n=0", "option=10", "hls=A_1-test", "ctok=",
	})...)
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			_, rejection := parseHLSViewRequest(newValidHLSViewRequest(http.MethodGet, raw, ""))
			assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
		})
	}
}

func TestParseHLSViewRequestRejectsMethodAndPath(t *testing.T) {
	for _, method := range []string{http.MethodHead, http.MethodPut, http.MethodDelete} {
		_, rejection := parseHLSViewRequest(newValidHLSViewRequest(method, validHLSViewQuery, ""))
		assertHLSRejection(t, rejection, http.StatusMethodNotAllowed, "method-not-allowed", "GET, POST")
	}
	for _, path := range []string{"/api/View", "/api/view/", "/api/view.json"} {
		request := newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")
		request.URL.Path = path
		_, rejection := parseHLSViewRequest(request)
		assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
	}
}

func TestParseHLSViewPOSTRejectsInvalidForm(t *testing.T) {
	validBodies := []string{"ctok=&open=1", "open=1&ctok="}
	for _, body := range validBodies {
		if _, rejection := parseHLSViewRequest(newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, body)); rejection != nil {
			t.Fatalf("valid body=%q rejection=%+v", body, rejection)
		}
	}

	invalidBodies := []string{
		"",
		"ctok=",
		"open=1",
		"ctok=token&open=1",
		"ctok=&open=",
		"ctok=&open=01",
		"ctok=&open=1&unknown=1",
		"ctok=&ctok=&open=1",
		"ctok=&open=1&open=1",
		"c%74ok=&open=1",
		"Ctok=&open=1",
		"ctok=&Open=1",
		"ctok=&open=%ZZ",
		"ctok=;open=1",
		"ctok=&open=1&",
		"ctok=&&open=1",
		"ctok=&open=1\n",
		"ctok=&open=1extra",
	}
	for _, body := range invalidBodies {
		t.Run(body, func(t *testing.T) {
			_, rejection := parseHLSViewRequest(newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, body))
			assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
		})
	}

	contentTypes := [][]string{
		{},
		{"application/x-www-form-urlencoded; charset=utf-8"},
		{"Application/X-Www-Form-Urlencoded"},
		{"text/plain"},
		{"application/x-www-form-urlencoded", "text/plain"},
	}
	for _, values := range contentTypes {
		request := newValidHLSViewRequest(http.MethodPost, validHLSViewQuery, validHLSViewBody)
		request.Header.Del("Content-Type")
		for _, value := range values {
			request.Header.Add("Content-Type", value)
		}
		_, rejection := parseHLSViewRequest(request)
		assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
	}
}

func TestReadHLSFormUsesDecodedOneKiBBoundary(t *testing.T) {
	exact := "x=" + strings.Repeat("a", hlsMaximumDecodedFormBytes-2)
	values, ok := readHLSForm(strings.NewReader(exact), int64(len(exact)))
	if !ok || len(values["x"]) != 1 || len(values["x"][0]) != hlsMaximumDecodedFormBytes-2 {
		t.Fatalf("exact decoded bytes=%d ok=%v values=%v", len(exact), ok, values)
	}
	oneOver := exact + "a"
	if _, ok := readHLSForm(strings.NewReader(oneOver), int64(len(oneOver))); ok {
		t.Fatal("decoded body over 1 KiB was accepted")
	}

	encodedExact := "x=" + strings.Repeat("%41", hlsMaximumDecodedFormBytes-2)
	values, ok = readHLSForm(strings.NewReader(encodedExact), int64(len(encodedExact)))
	if !ok || len(values["x"]) != 1 || len(values["x"][0]) != hlsMaximumDecodedFormBytes-2 {
		t.Fatalf("encoded exact raw=%d ok=%v", len(encodedExact), ok)
	}
	encodedOneOver := encodedExact + "%41"
	if _, ok := readHLSForm(strings.NewReader(encodedOneOver), int64(len(encodedOneOver))); ok {
		t.Fatal("encoded body with decoded length over 1 KiB was accepted")
	}

	tooMuchRaw := strings.Repeat("a", hlsMaximumEncodedFormBytes+1)
	if _, ok := readHLSForm(strings.NewReader(tooMuchRaw), -1); ok {
		t.Fatal("raw body over bounded encoded size was accepted")
	}
	if _, ok := readHLSForm(strings.NewReader(validHLSViewBody), int64(len(validHLSViewBody)+1)); ok {
		t.Fatal("truncated content length was accepted")
	}
	if _, ok := readHLSForm(failingHLSReader{}, -1); ok {
		t.Fatal("body read error was accepted")
	}
}

func TestHLSRequestsRequireExactlyOneEmptyCookie(t *testing.T) {
	valid := newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")
	valid.Header.Set("Cookie", "other=value; ctok=")
	if _, rejection := parseHLSViewRequest(valid); rejection != nil {
		t.Fatalf("valid cookies rejected: %+v", rejection)
	}

	cases := [][]string{
		{},
		{"other=value"},
		{"ctok=token"},
		{`ctok=""`},
		{"ctok=; ctok="},
		{"ctok=", "ctok="},
		{"CTOK="},
		{"ctok=; malformed"},
	}
	for _, headers := range cases {
		request := newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")
		request.Header.Del("Cookie")
		for _, value := range headers {
			request.Header.Add("Cookie", value)
		}
		_, rejection := parseHLSViewRequest(request)
		assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
	}
}

func TestParseHLSViewGETRejectsAnyBody(t *testing.T) {
	request := newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "x")
	_, rejection := parseHLSViewRequest(request)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")

	request = newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")
	request.Body = io.NopCloser(strings.NewReader("x"))
	request.ContentLength = 0
	_, rejection = parseHLSViewRequest(request)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")

	request = newValidHLSViewRequest(http.MethodGet, validHLSViewQuery, "")
	request.TransferEncoding = []string{"chunked"}
	_, rejection = parseHLSViewRequest(request)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
}

func TestParseHLSSegmentRequestAcceptsCanonicalPath(t *testing.T) {
	for _, test := range []struct {
		path     string
		key      string
		sequence uint64
	}{
		{path: "/komorebi/live/A/0.ts", key: "A", sequence: 0},
		{path: "/komorebi/live/A_1-test/7.ts", key: "A_1-test", sequence: 7},
		{path: "/komorebi/live/Z/18446744073709551615.ts", key: "Z", sequence: math.MaxUint64},
	} {
		request := newHLSRequest(http.MethodGet, test.path, "", "")
		request.Header.Set("Cookie", "ctok=")
		got, rejection := parseHLSSegmentRequest(request)
		want := hlsSegmentRequest{key: test.key, sequence: test.sequence}
		if rejection != nil || got != want {
			t.Fatalf("path=%q got=%+v rejection=%+v want=%+v", test.path, got, rejection, want)
		}
	}
}

func TestParseHLSSegmentRequestRejectsPathQueryAndCookie(t *testing.T) {
	invalidPaths := []string{
		"/komorebi/live/A/",
		"/komorebi/live/A/0",
		"/komorebi/live/A/0.TS",
		"/komorebi/live/A/00.ts",
		"/komorebi/live/A/-1.ts",
		"/komorebi/live/A/+1.ts",
		"/komorebi/live/A/18446744073709551616.ts",
		"/komorebi/live/A/1x.ts",
		"/komorebi/live/_A/1.ts",
		"/komorebi/live/A.B/1.ts",
		"/komorebi/live/A/B/1.ts",
		"/komorebi/Live/A/1.ts",
		"/komorebi/live/A/1.ts/",
	}
	for _, path := range invalidPaths {
		request := newHLSRequest(http.MethodGet, path, "", "")
		request.Header.Set("Cookie", "ctok=")
		_, rejection := parseHLSSegmentRequest(request)
		assertHLSRejection(t, rejection, http.StatusNotFound, "not-found", "")
	}

	encoded := newHLSRequest(http.MethodGet, "/komorebi/live/A/0.ts", "", "")
	encoded.URL.RawPath = "/komorebi/live/%41/0.ts"
	encoded.Header.Set("Cookie", "ctok=")
	_, rejection := parseHLSSegmentRequest(encoded)
	assertHLSRejection(t, rejection, http.StatusNotFound, "not-found", "")

	query := newHLSRequest(http.MethodGet, "/komorebi/live/A/0.ts", "unknown=1", "")
	query.Header.Set("Cookie", "ctok=")
	_, rejection = parseHLSSegmentRequest(query)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-query", "")

	trailingQuery := newHLSRequest(http.MethodGet, "/komorebi/live/A/0.ts", "", "")
	trailingQuery.URL.ForceQuery = true
	trailingQuery.Header.Set("Cookie", "ctok=")
	_, rejection = parseHLSSegmentRequest(trailingQuery)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-query", "")

	missingCookie := newHLSRequest(http.MethodGet, "/komorebi/live/A/0.ts", "", "")
	_, rejection = parseHLSSegmentRequest(missingCookie)
	assertHLSRejection(t, rejection, http.StatusBadRequest, "invalid-view-request", "")
}

func TestParseHLSSegmentRequestReportsMethod(t *testing.T) {
	request := newHLSRequest(http.MethodHead, "/komorebi/live/A/0.ts", "", "")
	request.Header.Set("Cookie", "ctok=")
	_, rejection := parseHLSSegmentRequest(request)
	assertHLSRejection(t, rejection, http.StatusMethodNotAllowed, "method-not-allowed", http.MethodGet)
}

func newHLSRequest(method, path, rawQuery, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.URL.RawQuery = rawQuery
	return request
}

func newValidHLSViewRequest(method, rawQuery, body string) *http.Request {
	request := newHLSRequest(method, "/api/view", rawQuery, body)
	request.Header.Set("Cookie", "ctok=")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return request
}

func assertHLSRejection(t *testing.T, got *hlsHTTPRejection, status int, reason, allow string) {
	t.Helper()
	if got == nil || got.status != status || got.reason != reason || got.allow != allow {
		t.Fatalf("rejection=%+v want_status=%d want_reason=%q want_allow=%q", got, status, reason, allow)
	}
}

func missingAndDuplicateHLSQueries(fields []string) []string {
	queries := make([]string, 0, len(fields)*2)
	for index, field := range fields {
		missing := make([]string, 0, len(fields)-1)
		missing = append(missing, fields[:index]...)
		missing = append(missing, fields[index+1:]...)
		queries = append(queries, strings.Join(missing, "&"))

		duplicate := append([]string(nil), fields...)
		duplicate = append(duplicate, field)
		queries = append(queries, strings.Join(duplicate, "&"))
	}
	return queries
}

type failingHLSReader struct{}

func (failingHLSReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
