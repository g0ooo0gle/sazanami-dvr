// Package recordinghttpは録画履歴、完成録画、Komorebi向けHTTPを公開する。
package recordinghttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	DefaultAddress = "127.0.0.1:4521"
	defaultLimit   = 50
	maximumLimit   = 100
	maximumStreams = 8
	maximumLogo    = 2 * 1024 * 1024
)

// Historyは録画結果のpage読出しと一件取得を提供する。
type History interface {
	RecordingHistory(context.Context, int, int32) ([]recording.HistoryItem, error)
	RecordingHistoryItem(context.Context, int32) (*recording.HistoryItem, error)
}

// FinalFileは絶対pathを公開せず、完成録画のRange読出しを提供する。
type FinalFile interface {
	io.ReadSeeker
	Close() error
	ModTime() time.Time
}

// Filesは検証済みの完成録画だけを開く録画保存先境界である。
type Files interface {
	OpenFinal(recording.FilePlan, int64) (FinalFile, error)
}

// LogoCatalogは完成済み番組表で放送IDを一つのMirakurunサービスへ解決する。
type LogoCatalog interface {
	ResolveLogoService(context.Context, uint16, uint16) (provider.TuningTarget, error)
}

// LogoProviderは検証済みサービスのPNG局ロゴだけを返す。
type LogoProvider interface {
	Logo(context.Context, provider.TuningTarget) ([]byte, error)
}

// Handlerは録画用REST API、Komorebi用API、完成録画とHLS配信を固定パスへ振り分ける。
// HLSを有効にした場合も、放送選択だけではMirakurunへ接続しない。
type Handler struct {
	history     History
	files       Files
	streams     chan struct{}
	placeholder []byte
	logoCatalog LogoCatalog
	logos       LogoProvider
	hls         *hlsSessionController
}

// NewHandlerは必須依存と同時配信上限を固定し、socketを作らずにhandlerを返す。
func NewHandler(history History, files Files) (*Handler, error) {
	return newHandler(history, files, nil, nil)
}

// NewHandlerWithLogosは録画配信にKomorebi向けの局ロゴ取得を加える。
func NewHandlerWithLogos(history History, files Files, catalog LogoCatalog, logos LogoProvider) (*Handler, error) {
	if catalog == nil || logos == nil {
		return nil, errors.New("recordinghttp: missing logo dependency")
	}
	return newHandler(history, files, catalog, logos)
}

// NewHandlerWithLiveは録画配信と局ロゴに、Komorebi向け原画質HLSを加える。
// キャッシュはデータ保存先内だけを使い、放送ストリームはPOST /api/viewまで開かない。
func NewHandlerWithLive(serviceContext context.Context, dataRoot string, history History, files Files,
	catalog LogoCatalog, logos LogoProvider, live LiveOperations,
) (*Handler, error) {
	if serviceContext == nil || catalog == nil || logos == nil || live == nil {
		return nil, errors.New("recordinghttp: missing live dependency")
	}
	handler, err := newHandler(history, files, catalog, logos)
	if err != nil {
		return nil, err
	}
	cache, err := openHLSCache(dataRoot, defaultHLSCacheLimits())
	if err != nil {
		return nil, errors.New("recordinghttp: hls cache unavailable")
	}
	handler.hls, err = newHLSSessionController(serviceContext, live, cache)
	if err != nil {
		_ = cache.close()
		return nil, err
	}
	return handler, nil
}

func newHandler(history History, files Files, catalog LogoCatalog, logos LogoProvider) (*Handler, error) {
	if history == nil || files == nil {
		return nil, errors.New("recordinghttp: missing dependency")
	}
	placeholder, err := placeholderPNG()
	if err != nil {
		return nil, errors.New("recordinghttp: placeholder generation failed")
	}
	return &Handler{history: history, files: files, streams: make(chan struct{}, maximumStreams), placeholder: placeholder,
		logoCatalog: catalog, logos: logos}, nil
}

// ServeHTTPは未知pathと変更methodを閉じ、内部errorの詳細を応答へ含めない。
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setHeaders(writer.Header())
	if handler == nil || request == nil {
		writeError(writer, http.StatusServiceUnavailable, "service-unavailable")
		return
	}
	if request.Context().Err() != nil {
		writeError(writer, http.StatusServiceUnavailable, "request-cancelled")
		return
	}
	switch {
	case handler.hls != nil && request.URL.Path == "/api/TvCast":
		handler.tvCast(writer, request)
	case handler.hls != nil && request.URL.Path == "/api/view":
		handler.view(writer, request)
	case handler.hls != nil && strings.HasPrefix(request.URL.Path, "/komorebi/live/"):
		handler.liveSegment(writer, request)
	case request.URL.Path == "/api/recordings":
		handler.list(writer, request)
	case strings.HasPrefix(request.URL.Path, "/api/recordings/"):
		handler.detail(writer, request)
	case request.URL.Path == "/komorebi/resolver.lua":
		handler.resolver(writer, request)
	case request.URL.Path == "/api/xcode":
		handler.xcode(writer, request)
	case request.URL.Path == "/legacy/logo.lua":
		handler.logo(writer, request)
	case strings.HasPrefix(request.URL.Path, "/recordings/"):
		handler.stream(writer, request)
	case request.URL.Path == "/api/Thumbnail":
		handler.thumbnail(writer, request)
	case strings.HasPrefix(request.URL.Path, "/komorebi/chapters/"):
		handler.chapters(writer, request)
	case strings.HasPrefix(request.URL.Path, "/komorebi/tiles/"):
		handler.tiles(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not-found")
	}
}

// CloseはHLSの配信処理、タイマー、キャッシュを閉じ、すべての終了を待つ。
// HTTP serverを先に停止して、新しい要求が入らない状態で呼ぶ。
func (handler *Handler) Close() error {
	if handler == nil || handler.hls == nil {
		return nil
	}
	return handler.hls.close()
}

func (handler *Handler) tvCast(writer http.ResponseWriter, request *http.Request) {
	selection, rejection := parseHLSTvCastRequest(request)
	if rejection == nil {
		rejection = handler.hls.selectLive(request.Context(), selection)
	}
	if rejection != nil {
		writeHLSRejection(writer, rejection)
		return
	}
	const response = `{"result":true}`
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(response)))
	_, _ = io.WriteString(writer, response)
}

func (handler *Handler) view(writer http.ResponseWriter, request *http.Request) {
	view, rejection := parseHLSViewRequest(request)
	if rejection != nil {
		writeHLSRejection(writer, rejection)
		return
	}
	if view.operation == hlsViewStart {
		if rejection = handler.hls.startLive(view); rejection != nil {
			writeHLSRejection(writer, rejection)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	playlist, rejection := handler.hls.playlistBytes(request.Context(), view)
	if rejection != nil {
		writeHLSRejection(writer, rejection)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	writer.Header().Set("Content-Length", strconv.Itoa(len(playlist)))
	_, _ = writer.Write(playlist)
}

func (handler *Handler) liveSegment(writer http.ResponseWriter, request *http.Request) {
	segment, rejection := parseHLSSegmentRequest(request)
	if rejection != nil {
		writeHLSRejection(writer, rejection)
		return
	}
	if !singleRange(writer, request) {
		return
	}
	file, rejection := handler.hls.openSegment(segment)
	if rejection != nil {
		writeHLSRejection(writer, rejection)
		return
	}
	defer file.Close()
	writer.Header().Set("Content-Type", "video/mp2t")
	http.ServeContent(writer, request, "segment.ts", file.ModTime(),
		contextReadSeeker{Context: request.Context(), ReadSeeker: file})
}

func writeHLSRejection(writer http.ResponseWriter, rejection *hlsHTTPRejection) {
	if rejection.allow != "" {
		writer.Header().Set("Allow", rejection.allow)
	}
	writeError(writer, rejection.status, rejection.reason)
}

func (handler *Handler) logo(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	networkID, serviceID, ok := logoQuery(request.URL.Query())
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid-query")
		return
	}
	if handler.logoCatalog == nil || handler.logos == nil {
		writeError(writer, http.StatusServiceUnavailable, "logo-unavailable")
		return
	}
	target, err := handler.logoCatalog.ResolveLogoService(request.Context(), networkID, serviceID)
	if err != nil {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	data, err := handler.logos.Logo(request.Context(), target)
	if err != nil {
		if provider.IsReason(err, provider.ReasonNotFound) {
			writeError(writer, http.StatusNotFound, "not-found")
		} else {
			writeError(writer, http.StatusServiceUnavailable, "logo-unavailable")
		}
		return
	}
	if len(data) < 8 || len(data) > maximumLogo || !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		writeError(writer, http.StatusServiceUnavailable, "logo-unavailable")
		return
	}
	writer.Header().Set("Content-Type", "image/png")
	writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if request.Method != http.MethodHead {
		_, _ = writer.Write(data)
	}
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	limit, before, ok := listQuery(request.URL.Query())
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid-query")
		return
	}
	items, err := handler.history.RecordingHistory(request.Context(), limit, before)
	if err != nil || len(items) > limit {
		writeError(writer, http.StatusServiceUnavailable, "history-unavailable")
		return
	}
	response := listResponse{Recordings: make([]recordingResponse, 0, len(items))}
	for _, item := range items {
		value, valid := project(item)
		if !valid {
			writeError(writer, http.StatusServiceUnavailable, "history-invalid")
			return
		}
		response.Recordings = append(response.Recordings, value)
	}
	if len(items) == limit {
		value := items[len(items)-1].Number
		response.NextBefore = &value
	}
	writeJSON(writer, request, response)
}

func (handler *Handler) detail(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if len(request.URL.Query()) != 0 {
		writeError(writer, http.StatusBadRequest, "invalid-query")
		return
	}
	id, ok := pathID(request.URL.Path, "/api/recordings/", "")
	if !ok {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	item, err := handler.history.RecordingHistoryItem(request.Context(), id)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "history-unavailable")
		return
	}
	if item == nil {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	value, valid := project(*item)
	if !valid {
		writeError(writer, http.StatusServiceUnavailable, "history-invalid")
		return
	}
	writeJSON(writer, request, value)
}

func (handler *Handler) resolver(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if request.URL.RawQuery == "" {
		writeJSONWithLength(writer, request, map[string]any{
			"ctok":   map[string]string{"xcode": "", "view": ""},
			"option": []map[string]string{{"id": "10", "name": "オリジナル（変換なし）"}},
		})
		return
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	id, ok := oneID(values)
	if !ok || !handler.playable(request.Context(), id) {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	base := strconv.FormatInt(int64(id), 10)
	writeJSON(writer, request, map[string]string{
		"video_url":       "/recordings/" + base + ".ts",
		"thumbnail_url":   "/api/Thumbnail?id=" + base,
		"chapter_url":     "/komorebi/chapters/" + base,
		"chapter_alt_url": "/komorebi/chapters/" + base,
		"tile_image_url":  "/api/Thumbnail?id=" + base,
		"tile_json_url":   "/komorebi/tiles/" + base + ".json",
	})
}

// xcodeは固定Komorebiが送る仮想pathまたは録画番号を検証し、原画質の完成録画だけを返す。
func (handler *Handler) xcode(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	id, reason := parseXcodeQuery(request.URL.RawQuery)
	if reason != "" {
		writeError(writer, http.StatusBadRequest, reason)
		return
	}
	if !singleRange(writer, request) {
		return
	}
	handler.serveFinalRecording(writer, request, id)
}

func (handler *Handler) stream(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if len(request.URL.Query()) != 0 {
		writeError(writer, http.StatusBadRequest, "invalid-query")
		return
	}
	if !singleRange(writer, request) {
		return
	}
	id, ok := pathID(request.URL.Path, "/recordings/", ".ts")
	if !ok {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	handler.serveFinalRecording(writer, request, id)
}

// serveFinalRecordingは検証済み録画番号を、二つのURLで共有する完成条件と配信枠から読み出す。
func (handler *Handler) serveFinalRecording(writer http.ResponseWriter, request *http.Request, id int32) {
	select {
	case handler.streams <- struct{}{}:
		defer func() { <-handler.streams }()
	default:
		writeError(writer, http.StatusServiceUnavailable, "stream-limit")
		return
	}
	item, err := handler.history.RecordingHistoryItem(request.Context(), id)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "history-unavailable")
		return
	}
	if item == nil || !item.Playable() {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	file, err := handler.files.OpenFinal(item.Plan, item.ByteCount)
	if err != nil {
		writeError(writer, http.StatusNotFound, "file-unavailable")
		return
	}
	defer file.Close()
	writer.Header().Set("Content-Type", "video/mp2t")
	http.ServeContent(writer, request, "recording.ts", file.ModTime(), contextReadSeeker{Context: request.Context(), ReadSeeker: file})
}

func (handler *Handler) thumbnail(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	id, ok := oneID(request.URL.Query())
	if !ok || !handler.playable(request.Context(), id) {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	writer.Header().Set("Content-Type", "image/png")
	writer.Header().Set("Content-Length", strconv.Itoa(len(handler.placeholder)))
	if request.Method != http.MethodHead {
		_, _ = writer.Write(handler.placeholder)
	}
}

func (handler *Handler) chapters(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if len(request.URL.Query()) != 0 {
		writeError(writer, http.StatusBadRequest, "invalid-query")
		return
	}
	id, ok := pathID(request.URL.Path, "/komorebi/chapters/", "")
	if !ok || !handler.playable(request.Context(), id) {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Content-Length", "0")
}

func (handler *Handler) tiles(writer http.ResponseWriter, request *http.Request) {
	if !readMethod(writer, request) {
		return
	}
	if len(request.URL.Query()) != 0 {
		writeError(writer, http.StatusBadRequest, "invalid-query")
		return
	}
	id, ok := pathID(request.URL.Path, "/komorebi/tiles/", ".json")
	if !ok || !handler.playable(request.Context(), id) {
		writeError(writer, http.StatusNotFound, "not-found")
		return
	}
	writeJSON(writer, request, map[string]any{"image_width": 320, "image_height": 180, "tile_width": 320,
		"tile_height": 180, "column_count": 1, "row_count": 1, "interval_sec": 10, "total_tiles": 1})
}

func (handler *Handler) playable(ctx context.Context, id int32) bool {
	item, err := handler.history.RecordingHistoryItem(ctx, id)
	return err == nil && item != nil && item.Playable()
}

type listResponse struct {
	Recordings []recordingResponse `json:"recordings"`
	NextBefore *int32              `json:"next_before"`
}
type recordingResponse struct {
	ID                int32                    `json:"id"`
	State             recording.AttemptState   `json:"state"`
	Reason            recording.TerminalReason `json:"reason"`
	Title             string                   `json:"title"`
	StationName       string                   `json:"station_name"`
	NetworkID         uint16                   `json:"network_id"`
	TransportStreamID uint16                   `json:"transport_stream_id"`
	ServiceID         uint16                   `json:"service_id"`
	EventID           uint16                   `json:"event_id"`
	PlannedStart      string                   `json:"planned_start"`
	PlannedEnd        string                   `json:"planned_end"`
	ActualStart       *string                  `json:"actual_start"`
	ActualEnd         *string                  `json:"actual_end"`
	ByteCount         int64                    `json:"byte_count"`
	Playable          bool                     `json:"playable"`
	PlaybackURL       *string                  `json:"playback_url"`
}

func project(item recording.HistoryItem) (recordingResponse, bool) {
	if item.Validate() != nil {
		return recordingResponse{}, false
	}
	value := recordingResponse{ID: item.Number, State: item.State, Reason: item.Reason, Title: item.Title, StationName: item.StationName,
		NetworkID: item.NetworkID, TransportStreamID: item.TransportStreamID, ServiceID: item.ServiceID, EventID: item.EventID,
		PlannedStart: item.PlannedStart.Format(time.RFC3339), PlannedEnd: item.PlannedEnd.Format(time.RFC3339), ByteCount: item.ByteCount, Playable: item.Playable()}
	if item.ActualStart != nil {
		text := item.ActualStart.Format(time.RFC3339)
		value.ActualStart = &text
	}
	if item.ActualEnd != nil {
		text := item.ActualEnd.Format(time.RFC3339)
		value.ActualEnd = &text
	}
	if value.Playable {
		text := "/recordings/" + strconv.FormatInt(int64(item.Number), 10) + ".ts"
		value.PlaybackURL = &text
	}
	return value, true
}

func listQuery(values url.Values) (int, int32, bool) {
	for key, entries := range values {
		if (key != "limit" && key != "before") || len(entries) != 1 || entries[0] == "" {
			return 0, 0, false
		}
	}
	limit := defaultLimit
	if raw := values.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumLimit {
			return 0, 0, false
		}
		limit = value
	}
	var before int32
	if raw := values.Get("before"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || value < 1 {
			return 0, 0, false
		}
		before = int32(value)
	}
	return limit, before, true
}

func oneID(values url.Values) (int32, bool) {
	if len(values) != 1 {
		return 0, false
	}
	entries, ok := values["id"]
	if !ok || len(entries) != 1 || entries[0] == "" {
		return 0, false
	}
	value, err := strconv.ParseInt(entries[0], 10, 32)
	return int32(value), err == nil && value > 0
}

func logoQuery(values url.Values) (uint16, uint16, bool) {
	if len(values) != 2 {
		return 0, 0, false
	}
	parse := func(key string) (uint16, bool) {
		entries, ok := values[key]
		if !ok || len(entries) != 1 || entries[0] == "" {
			return 0, false
		}
		value, err := strconv.ParseUint(entries[0], 10, 16)
		return uint16(value), err == nil && value > 0 && strconv.FormatUint(value, 10) == entries[0]
	}
	networkID, networkOK := parse("onid")
	serviceID, serviceOK := parse("sid")
	return networkID, serviceID, networkOK && serviceOK
}

func pathID(path, prefix, suffix string) (int32, bool) {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	return int32(value), err == nil && value > 0
}

// parseXcodeQueryは標準URL decode後の仮想pathと互換値を厳密に検証し、host pathを組み立てない。
func parseXcodeQuery(raw string) (int32, string) {
	if raw == "" {
		return 0, "invalid-query"
	}
	for _, field := range strings.Split(raw, "&") {
		if field == "" || strings.HasPrefix(field, "=") {
			return 0, "invalid-query"
		}
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "invalid-query"
	}
	for key := range values {
		switch key {
		case "fname", "id", "option", "ctok", "ofssec":
		default:
			return 0, "invalid-query"
		}
	}

	fname, hasFName := values["fname"]
	rawID, hasID := values["id"]
	if hasFName == hasID || (hasFName && len(fname) != 1) || (hasID && len(rawID) != 1) {
		return 0, "invalid-query"
	}
	var id int32
	var ok bool
	if hasFName {
		id, ok = xcodeFileID(fname[0])
	} else {
		id, ok = canonicalRecordingID(rawID[0])
	}
	if !ok {
		return 0, "invalid-query"
	}

	option := values["option"]
	if len(option) != 1 || option[0] != "10" {
		return 0, "unsupported-option"
	}
	if token, exists := values["ctok"]; exists && (len(token) != 1 || token[0] != "") {
		return 0, "invalid-token"
	}
	if offset, exists := values["ofssec"]; exists && (len(offset) != 1 || !validXcodeOffset(offset[0])) {
		return 0, "invalid-offset"
	}
	return id, ""
}

// xcodeFileIDは公開した仮想pathだけから録画番号を取り出す。
func xcodeFileID(value string) (int32, bool) {
	const prefix, suffix = "recordings/", ".ts"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	if raw == "" || strings.Contains(raw, "/") {
		return 0, false
	}
	return canonicalRecordingID(raw)
}

// canonicalRecordingIDは表記揺れのない正の32 bit録画番号だけを受ける。
func canonicalRecordingID(raw string) (int32, bool) {
	value, err := strconv.ParseInt(raw, 10, 32)
	return int32(value), err == nil && value > 0 && strconv.FormatInt(value, 10) == raw
}

// validXcodeOffsetは固定クライアントが送る再生秒を検証する。配信位置には使わない。
func validXcodeOffset(raw string) bool {
	value, err := strconv.ParseUint(raw, 10, 32)
	return err == nil && value <= 86_400 && strconv.FormatUint(value, 10) == raw
}

// singleRangeは二つの録画URLで同じ複数Range拒否を使う。
func singleRange(writer http.ResponseWriter, request *http.Request) bool {
	ranges := request.Header.Values("Range")
	if len(ranges) > 1 || (len(ranges) == 1 && strings.Contains(ranges[0], ",")) {
		writeError(writer, http.StatusRequestedRangeNotSatisfiable, "multiple-ranges-unsupported")
		return false
	}
	return true
}

// contextReadSeekerは途中で取り消された要求の次のfile読出しを止める。
type contextReadSeeker struct {
	context.Context
	io.ReadSeeker
}

// Readは要求の取消しを確認してから完成fileを読む。
func (reader contextReadSeeker) Read(data []byte) (int, error) {
	if err := reader.Context.Err(); err != nil {
		return 0, err
	}
	return reader.ReadSeeker.Read(data)
}

func readMethod(writer http.ResponseWriter, request *http.Request) bool {
	if request.Method == http.MethodGet || request.Method == http.MethodHead {
		return true
	}
	writer.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
	writeError(writer, http.StatusMethodNotAllowed, "method-not-allowed")
	return false
}

func writeJSON(writer http.ResponseWriter, request *http.Request, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	if request.Method == http.MethodHead {
		return
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return
	}
}

// writeJSONWithLengthはresolver設定のGETとHEADへ同じ長さを明示する。
func writeJSONWithLength(writer http.ResponseWriter, request *http.Request, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "service-unavailable")
		return
	}
	data = append(data, '\n')
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if request.Method != http.MethodHead {
		_, _ = writer.Write(data)
	}
}

func writeError(writer http.ResponseWriter, status int, reason string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, reason+"\n")
}

func setHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
}

func placeholderPNG() ([]byte, error) {
	imageValue := image.NewRGBA(image.Rect(0, 0, 320, 180))
	background := color.RGBA{R: 32, G: 45, B: 58, A: 255}
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			imageValue.SetRGBA(x, y, background)
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, imageValue); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
