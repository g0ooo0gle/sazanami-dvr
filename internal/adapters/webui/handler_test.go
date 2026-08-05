package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/app/opsui"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestRoutesMethodsAndSecurityHeaders(t *testing.T) {
	handler := newTestHandler(t, &fakeApplication{})
	for _, path := range []string{"/", "/settings", "/epg", "/operations", "/assets/style.css"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := perform(handler, method, path, "127.0.0.1:40772", nil)
			if response.Code != http.StatusOK {
				t.Errorf("%s %s status=%d", method, path, response.Code)
			}
			if method == http.MethodHead && response.Body.Len() != 0 {
				t.Errorf("HEAD %s body=%q", path, response.Body.String())
			}
			assertSecurityHeaders(t, response)
		}
	}
	unknown := perform(handler, http.MethodGet, "/unknown", "127.0.0.1:40772", nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status=%d", unknown.Code)
	}
	method := perform(handler, http.MethodPost, "/settings", "127.0.0.1:40772", nil)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("method status=%d allow=%q", method.Code, method.Header().Get("Allow"))
	}
}

func TestHostMustBeNumericLoopback(t *testing.T) {
	handler := newTestHandler(t, &fakeApplication{})
	accepted := []string{"127.0.0.1", "127.0.0.1:40772", "[::1]:40772"}
	for _, host := range accepted {
		if got := perform(handler, http.MethodGet, "/", host, nil).Code; got != http.StatusOK {
			t.Errorf("host %q status=%d", host, got)
		}
	}
	rejected := []string{"localhost:40772", "0.0.0.0:40772", "192.0.2.39:40772", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:bad"}
	for _, host := range rejected {
		if got := perform(handler, http.MethodGet, "/", host, nil).Code; got != http.StatusMisdirectedRequest {
			t.Errorf("host %q status=%d", host, got)
		}
	}
}

func TestBackupRequiresOriginCSRFAndBoundedForm(t *testing.T) {
	application := &fakeApplication{}
	handler := newTestHandler(t, application)
	token := extractToken(t, perform(handler, http.MethodGet, "/operations", "127.0.0.1:40772", nil).Body.String())

	valid := url.Values{"csrf": {token}}.Encode()
	response := postForm(handler, valid, "http://127.0.0.1:40772")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "complete") || application.backups != 1 {
		t.Fatalf("valid status=%d body=%q backups=%d", response.Code, response.Body.String(), application.backups)
	}

	for name, test := range map[string]struct {
		body   string
		origin string
		status int
	}{
		"foreign-origin": {valid, "http://127.0.0.1:40773", http.StatusForbidden},
		"https-origin":   {valid, "https://127.0.0.1:40772", http.StatusForbidden},
		"bad-token":      {"csrf=" + strings.Repeat("0", 64), "http://127.0.0.1:40772", http.StatusForbidden},
		"unknown-field":  {valid + "&extra=1", "http://127.0.0.1:40772", http.StatusBadRequest},
		"duplicate":      {valid + "&csrf=" + token, "http://127.0.0.1:40772", http.StatusBadRequest},
		"too-large":      {"csrf=" + token + strings.Repeat("x", 4096), "http://127.0.0.1:40772", http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			got := postForm(handler, test.body, test.origin)
			if got.Code != test.status {
				t.Fatalf("status=%d body=%q", got.Code, got.Body.String())
			}
		})
	}
	if application.backups != 1 {
		t.Fatalf("rejected form invoked backup: %d", application.backups)
	}
}

func TestEPGEscapesHostileTitleAndRejectsQuery(t *testing.T) {
	title := `<script>alert("x")</script><img src=x onerror=alert(1)>`
	application := &fakeApplication{guide: testGuide(t, title)}
	handler := newTestHandler(t, application)
	response := perform(handler, http.MethodGet, "/epg", "127.0.0.1:40772", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	body := response.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, "<img") || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("hostile title was not escaped: %q", body)
	}
	bad := perform(handler, http.MethodGet, "/epg?unexpected=1", "127.0.0.1:40772", nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad query status=%d", bad.Code)
	}
}

func TestEPGBackendNotFoundAndApplicationCancellation(t *testing.T) {
	handler := newTestHandler(t, &fakeApplication{guideErr: opsui.ErrBackendNotFound})
	if got := perform(handler, http.MethodGet, "/epg", "127.0.0.1:40772", nil).Code; got != http.StatusNotFound {
		t.Fatalf("backend status=%d", got)
	}
	application := &fakeApplication{waitForCancel: true}
	handler = newTestHandler(t, application)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:40772/", nil)
	request.Host = "127.0.0.1:40772"
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(ctx))
	if response.Code != http.StatusServiceUnavailable || !application.cancelled {
		t.Fatalf("cancel status=%d observed=%v", response.Code, application.cancelled)
	}
}

func TestAccessLogDoesNotContainRequestSecrets(t *testing.T) {
	var output strings.Builder
	handler, err := NewHandler(&fakeApplication{}, &output)
	if err != nil {
		t.Fatal(err)
	}
	_ = perform(handler, http.MethodGet, "/epg?from=secret", "127.0.0.1:40772", nil)
	logText := output.String()
	if strings.Contains(logText, "secret") || strings.Contains(logText, "127.0.0.1") || !strings.Contains(logText, "route=epg") {
		t.Fatalf("unsafe log=%q", logText)
	}
}

func newTestHandler(t *testing.T, application Application) *Handler {
	t.Helper()
	handler, err := NewHandler(application, nil)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func perform(handler http.Handler, method, target, host string, body *strings.Reader) *httptest.ResponseRecorder {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, "http://127.0.0.1:40772"+target, nil)
	} else {
		request = httptest.NewRequest(method, "http://127.0.0.1:40772"+target, body)
	}
	request.Host = host
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func postForm(handler http.Handler, body, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:40772/operations/backup", strings.NewReader(body))
	request.Host = "127.0.0.1:40772"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func extractToken(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="csrf" value="([0-9a-f]{64})"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("csrf token not found: %q", body)
	}
	return match[1]
}

func assertSecurityHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for _, name := range []string{"Content-Type", "Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if response.Header().Get(name) == "" {
			t.Errorf("missing header %s", name)
		}
	}
}

func testGuide(t *testing.T, title string) opsui.Guide {
	t.Helper()
	id, err := catalogmodel.ParseID("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	return opsui.Guide{
		Backends: []opsui.Backend{{ID: id, Kind: "FAKE"}}, Selected: id, From: start, To: end,
		Programs: []opsui.Program{{InstanceID: id, ServiceName: "合成サービス", Start: start, End: &end, Title: &title}},
	}
}

type fakeApplication struct {
	guide         opsui.Guide
	guideErr      error
	backupErr     error
	backups       int
	waitForCancel bool
	cancelled     bool
}

func (application *fakeApplication) Overview(ctx context.Context) (opsui.Overview, error) {
	if application.waitForCancel {
		<-ctx.Done()
		application.cancelled = true
		return opsui.Overview{}, ctx.Err()
	}
	return opsui.Overview{Settings: application.Settings(), Backends: application.guide.Backends}, nil
}

func (application *fakeApplication) Settings() opsui.Settings {
	return opsui.Settings{ProductVersion: "test", ProductCommit: "0123456789abcdef", SchemaCurrent: 1, SchemaTarget: 1, ListenScope: "loopback-only"}
}

func (application *fakeApplication) Guide(context.Context, opsui.GuideRequest) (opsui.Guide, error) {
	if application.guideErr != nil {
		return opsui.Guide{}, application.guideErr
	}
	return application.guide, nil
}

func (application *fakeApplication) Backup(context.Context) (opsui.BackupResult, error) {
	application.backups++
	if application.backupErr != nil {
		return opsui.BackupResult{}, application.backupErr
	}
	return opsui.BackupResult{ID: "00000000-0000-4000-8000-000000000002", State: "complete", SchemaVersion: 1}, nil
}
