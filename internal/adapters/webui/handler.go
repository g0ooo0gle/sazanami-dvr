// Package webuiはloopback限定の運用WebUIとHTTP防御境界を実装する。
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/app/opsui"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	maximumFormBytes = 4 * 1024
	requestTimeout   = 10 * time.Second
	contentPolicy    = "default-src 'none'; style-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
)

//go:embed templates/*.html assets/style.css
var files embed.FS

// ApplicationはHTTPから呼べる運用use caseを限定する。
type Application interface {
	Overview(context.Context) (opsui.Overview, error)
	Settings() opsui.Settings
	Guide(context.Context, opsui.GuideRequest) (opsui.Guide, error)
	Backup(context.Context) (opsui.BackupResult, error)
}

// Handlerはloopback Host、CSRF、response limitを強制するHTTP handlerである。
type Handler struct {
	application Application
	templates   *template.Template
	csrfToken   string
	logger      *log.Logger
}

// NewHandlerはprocess-local CSRF tokenを生成してhandlerを構成する。乱数失敗時は起動しない。
func NewHandler(application Application, accessLog io.Writer) (*Handler, error) {
	if application == nil {
		return nil, errors.New("webui: missing application")
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return nil, errors.New("webui: csrf generation failed")
	}
	parsed, err := template.New("pages").Funcs(template.FuncMap{
		"jst": func(value time.Time) string {
			return value.In(time.FixedZone("Asia/Tokyo", 9*60*60)).Format("2006-01-02 15:04:05")
		},
	}).ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, errors.New("webui: template parse failed")
	}
	if accessLog == nil {
		accessLog = io.Discard
	}
	return &Handler{
		application: application,
		templates:   parsed,
		csrfToken:   hex.EncodeToString(tokenBytes),
		logger:      log.New(accessLog, "", 0),
	}, nil
}

// ServeHTTPは全routeへ共通security header、Host検証、bounded timeoutを適用する。
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	tracked := &statusWriter{ResponseWriter: writer, status: http.StatusOK}
	setSecurityHeaders(tracked.Header())
	routeID := routeName(request.URL.Path)
	reason := "ok"
	defer func() {
		handler.logger.Printf("method=%s route=%s status=%d duration_ms=%d reason=%s",
			request.Method, routeID, tracked.status, time.Since(started).Milliseconds(), reason)
	}()
	if _, _, err := loopbackAuthority(request.Host); err != nil {
		reason = "invalid-host"
		writeError(tracked, request.Method, http.StatusMisdirectedRequest, "要求先を確認できません")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
	defer cancel()
	request = request.WithContext(ctx)

	switch request.URL.Path {
	case "/":
		reason = handler.overview(tracked, request)
	case "/settings":
		reason = handler.settings(tracked, request)
	case "/epg":
		reason = handler.epg(tracked, request)
	case "/operations":
		reason = handler.operations(tracked, request, opsui.BackupResult{}, "")
	case "/operations/backup":
		reason = handler.backup(tracked, request)
	case "/assets/style.css":
		reason = handler.stylesheet(tracked, request)
	default:
		reason = "not-found"
		writeError(tracked, request.Method, http.StatusNotFound, "ページが見つかりません")
	}
}

func (handler *Handler) overview(writer http.ResponseWriter, request *http.Request) string {
	if !allowReadMethod(writer, request.Method) {
		return "method-not-allowed"
	}
	value, err := handler.application.Overview(request.Context())
	if err != nil {
		writeError(writer, request.Method, http.StatusServiceUnavailable, "状態を取得できません")
		return "overview-unavailable"
	}
	data := struct {
		Overview opsui.Overview
		CSRF     string
	}{value, handler.csrfToken}
	return handler.render(writer, request.Method, "overview.html", data)
}

func (handler *Handler) settings(writer http.ResponseWriter, request *http.Request) string {
	if !allowReadMethod(writer, request.Method) {
		return "method-not-allowed"
	}
	data := struct {
		Settings opsui.Settings
		CSRF     string
	}{handler.application.Settings(), handler.csrfToken}
	return handler.render(writer, request.Method, "settings.html", data)
}

func (handler *Handler) epg(writer http.ResponseWriter, request *http.Request) string {
	if !allowReadMethod(writer, request.Method) {
		return "method-not-allowed"
	}
	query, err := parseGuideQuery(request.URL.Query())
	if err != nil {
		writeError(writer, request.Method, http.StatusBadRequest, "EPGの検索条件が正しくありません")
		return "invalid-epg-query"
	}
	guide, err := handler.application.Guide(request.Context(), query)
	if errors.Is(err, opsui.ErrBackendNotFound) {
		writeError(writer, request.Method, http.StatusNotFound, "指定されたバックエンドが見つかりません")
		return "backend-not-found"
	}
	if err != nil {
		writeError(writer, request.Method, http.StatusServiceUnavailable, "EPGを取得できません")
		return "epg-unavailable"
	}
	data := guidePage{Guide: guide, CSRF: handler.csrfToken}
	for _, backend := range guide.Backends {
		data.Backends = append(data.Backends, backendOption{
			ID: backend.ID.String(), Kind: backend.Kind, Selected: backend.ID == guide.Selected,
		})
	}
	return handler.render(writer, request.Method, "epg.html", data)
}

func (handler *Handler) operations(writer http.ResponseWriter, request *http.Request, result opsui.BackupResult, failure string) string {
	if !allowReadMethod(writer, request.Method) {
		return "method-not-allowed"
	}
	data := operationsPage{CSRF: handler.csrfToken, Result: result, Failure: failure}
	return handler.render(writer, request.Method, "operations.html", data)
}

func (handler *Handler) backup(writer http.ResponseWriter, request *http.Request) string {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, request.Method, http.StatusMethodNotAllowed, "この操作ではPOSTだけを使用できます")
		return "method-not-allowed"
	}
	if originReason := rejectedOriginReason(request); originReason != "" {
		writeError(writer, request.Method, http.StatusForbidden, "送信元を確認できません")
		return originReason
	}
	token, reason, status := readCSRFForm(request)
	if status != 0 {
		writeError(writer, request.Method, status, "送信内容を確認できません")
		return reason
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || hex.EncodeToString(decoded) != token ||
		subtle.ConstantTimeCompare(decoded, mustDecodeToken(handler.csrfToken)) != 1 {
		writeError(writer, request.Method, http.StatusForbidden, "送信内容を確認できません")
		return "csrf-rejected"
	}
	result, err := handler.application.Backup(request.Context())
	if err != nil {
		failure := "backup-failed"
		status := http.StatusServiceUnavailable
		if errors.Is(err, opsui.ErrBackupBusy) {
			failure = "backup-busy"
			status = http.StatusConflict
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.WriteHeader(status)
		return handler.renderBody(writer, request.Method, "operations.html",
			operationsPage{CSRF: handler.csrfToken, Failure: failure})
	}
	return handler.render(writer, request.Method, "operations.html",
		operationsPage{CSRF: handler.csrfToken, Result: result})
}

func (handler *Handler) stylesheet(writer http.ResponseWriter, request *http.Request) string {
	if !allowReadMethod(writer, request.Method) {
		return "method-not-allowed"
	}
	content, err := files.ReadFile("assets/style.css")
	if err != nil {
		writeError(writer, request.Method, http.StatusInternalServerError, "表示資材を取得できません")
		return "asset-unavailable"
	}
	writer.Header().Set("Content-Type", "text/css; charset=utf-8")
	if request.Method != http.MethodHead {
		_, _ = writer.Write(content)
	}
	return "ok"
}

func (handler *Handler) render(writer http.ResponseWriter, method, name string, value any) string {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := handler.renderBody(writer, method, name, value); err != "ok" {
		return err
	}
	return "ok"
}

func (handler *Handler) renderBody(writer http.ResponseWriter, method, name string, value any) string {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	if method == http.MethodHead {
		return "ok"
	}
	if err := handler.templates.ExecuteTemplate(writer, name, value); err != nil {
		return "template-failed"
	}
	return "ok"
}

type backendOption struct {
	ID       string
	Kind     string
	Selected bool
}

type guidePage struct {
	opsui.Guide
	Backends []backendOption
	CSRF     string
}

type operationsPage struct {
	CSRF    string
	Result  opsui.BackupResult
	Failure string
}

func parseGuideQuery(values url.Values) (opsui.GuideRequest, error) {
	for key, value := range values {
		if key != "backend" && key != "from" && key != "to" {
			return opsui.GuideRequest{}, errors.New("unknown query")
		}
		if len(value) != 1 || value[0] == "" {
			return opsui.GuideRequest{}, errors.New("invalid query")
		}
	}
	var request opsui.GuideRequest
	if raw := values.Get("backend"); raw != "" {
		id, err := catalogmodel.ParseID(raw)
		if err != nil {
			return request, err
		}
		request.Backend = &id
	}
	for key, target := range map[string]**time.Time{"from": &request.From, "to": &request.To} {
		if raw := values.Get(key); raw != "" {
			value, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return request, err
			}
			*target = &value
		}
	}
	return request, nil
}

func allowReadMethod(writer http.ResponseWriter, method string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
	writeError(writer, method, http.StatusMethodNotAllowed, "このページではGETまたはHEADだけを使用できます")
	return false
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", contentPolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
}

func writeError(writer http.ResponseWriter, method string, status int, message string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	if method != http.MethodHead {
		_, _ = fmt.Fprintln(writer, message)
	}
}

func routeName(path string) string {
	switch path {
	case "/":
		return "overview"
	case "/settings":
		return "settings"
	case "/epg":
		return "epg"
	case "/operations":
		return "operations"
	case "/operations/backup":
		return "backup"
	case "/assets/style.css":
		return "style"
	default:
		return "unknown"
	}
}

func loopbackAuthority(authority string) (netip.Addr, string, error) {
	host := authority
	port := "80"
	if strings.Contains(authority, ":") {
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return netip.Addr{}, "", errors.New("invalid authority")
		}
	}
	address, err := netip.ParseAddr(host)
	if err != nil || !address.IsLoopback() {
		return netip.Addr{}, "", errors.New("not loopback")
	}
	if port == "" {
		return netip.Addr{}, "", errors.New("missing port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return netip.Addr{}, "", errors.New("invalid port")
	}
	return address, port, nil
}

func rejectedOriginReason(request *http.Request) string {
	raw := request.Header.Get("Origin")
	if raw == "" {
		return ""
	}
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme != "http" || origin.User != nil || origin.Path != "" ||
		origin.RawQuery != "" || origin.Fragment != "" {
		return "origin-format-rejected"
	}
	requestIP, requestPort, err := loopbackAuthority(request.Host)
	if err != nil {
		return "origin-request-authority-rejected"
	}
	originIP, originPort, err := loopbackAuthority(origin.Host)
	if err != nil {
		return "origin-authority-rejected"
	}
	if requestIP != originIP || requestPort != originPort {
		return "origin-mismatch-rejected"
	}
	return ""
}

func readCSRFForm(request *http.Request) (string, string, int) {
	contentType := request.Header.Get("Content-Type")
	if contentType != "application/x-www-form-urlencoded" {
		return "", "invalid-content-type", http.StatusBadRequest
	}
	content, err := io.ReadAll(io.LimitReader(request.Body, maximumFormBytes+1))
	if err != nil || len(content) > maximumFormBytes {
		return "", "body-too-large", http.StatusBadRequest
	}
	values, err := url.ParseQuery(string(content))
	if err != nil || len(values) != 1 {
		return "", "invalid-form", http.StatusBadRequest
	}
	tokens, ok := values["csrf"]
	if !ok || len(tokens) != 1 || tokens[0] == "" {
		return "", "invalid-form", http.StatusBadRequest
	}
	return tokens[0], "", 0
}

func mustDecodeToken(value string) []byte {
	result, _ := hex.DecodeString(value)
	return result
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeaderはaccess log用に最終HTTP statusを記録して転送する。
func (writer *statusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}
