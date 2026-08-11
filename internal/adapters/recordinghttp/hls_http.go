package recordinghttp

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	hlsMaximumDecodedFormBytes = 1024
	hlsMaximumEncodedFormBytes = hlsMaximumDecodedFormBytes * 3
)

// hlsServiceIDはTvCastとviewが共有する放送IDを、三つの16 bit値に分けて保持する。
type hlsServiceID struct {
	onid uint16
	tsid uint16
	sid  uint16
}

// hlsTvCastRequestはproviderを開く前に保存する、放送と表示枠の選択である。
type hlsTvCastRequest struct {
	service hlsServiceID
	slot    uint8
}

// hlsViewOperationは同じview URLに対する開始POSTとプレイリストGETを区別する。
type hlsViewOperation uint8

const (
	hlsViewStart hlsViewOperation = iota + 1
	hlsViewPlaylist
)

// hlsViewRequestはPOSTによる開始とGETによるプレイリスト取得に共通する入力である。
type hlsViewRequest struct {
	service   hlsServiceID
	slot      uint8
	key       string
	operation hlsViewOperation
}

// hlsSegmentRequestは公開URLから検証済みkeyと再利用しない連番だけを取り出す。
type hlsSegmentRequest struct {
	key      string
	sequence uint64
}

// hlsHTTPRejectionはhandlerが固定status、理由、Allow headerをそのまま応答へ使うための結果である。
// 入力値は保持せず、放送IDやHLS keyを誤って出力できない形にする。
type hlsHTTPRejection struct {
	status int
	reason string
	allow  string
}

// parseHLSTvCastRequestは固定TvCast path、GET、query四項目を表記まで含めて検証する。
func parseHLSTvCastRequest(request *http.Request) (hlsTvCastRequest, *hlsHTTPRejection) {
	if request == nil {
		return hlsTvCastRequest{}, rejectHLS(http.StatusBadRequest, "invalid-query", "")
	}
	if request.Method != http.MethodGet {
		return hlsTvCastRequest{}, rejectHLS(http.StatusMethodNotAllowed, "method-not-allowed", http.MethodGet)
	}
	if request.URL == nil || request.URL.Path != "/api/TvCast" || request.URL.RawPath != "" {
		return hlsTvCastRequest{}, rejectHLS(http.StatusBadRequest, "invalid-query", "")
	}
	values, ok := parseStrictHLSValues(request.URL.RawQuery)
	if !ok || len(values) != 4 {
		return hlsTvCastRequest{}, rejectHLS(http.StatusBadRequest, "invalid-query", "")
	}
	service, serviceOK := oneHLSServiceID(values, "id")
	slot, slotOK := oneHLSSlot(values, "n")
	jsonValue, jsonOK := oneHLSValue(values, "json")
	token, tokenOK := oneHLSValue(values, "ctok")
	if !serviceOK || !slotOK || !jsonOK || jsonValue != "1" || !tokenOK || token != "" {
		return hlsTvCastRequest{}, rejectHLS(http.StatusBadRequest, "invalid-query", "")
	}
	return hlsTvCastRequest{service: service, slot: slot}, nil
}

// parseHLSViewRequestはGETとPOSTに共通するqueryを検証し、method固有のbodyとCookieも確認する。
func parseHLSViewRequest(request *http.Request) (hlsViewRequest, *hlsHTTPRejection) {
	if request == nil {
		return hlsViewRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	var operation hlsViewOperation
	switch request.Method {
	case http.MethodPost:
		operation = hlsViewStart
	case http.MethodGet:
		operation = hlsViewPlaylist
	default:
		return hlsViewRequest{}, rejectHLS(http.StatusMethodNotAllowed, "method-not-allowed", http.MethodGet+", "+http.MethodPost)
	}
	if request.URL == nil || request.URL.Path != "/api/view" || request.URL.RawPath != "" {
		return hlsViewRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	values, ok := parseStrictHLSValues(request.URL.RawQuery)
	if !ok || len(values) != 5 {
		return hlsViewRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	service, serviceOK := oneHLSServiceID(values, "id")
	slot, slotOK := oneHLSSlot(values, "n")
	option, optionOK := oneHLSValue(values, "option")
	key, keyOK := oneHLSValue(values, "hls")
	token, tokenOK := oneHLSValue(values, "ctok")
	if !serviceOK || !slotOK || !optionOK || option != "10" || !keyOK || !validHLSKey(key) ||
		!tokenOK || token != "" || !validEmptyHLSCookie(request) {
		return hlsViewRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	if operation == hlsViewStart {
		if !validHLSViewForm(request) {
			return hlsViewRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
		}
	} else if !emptyHLSBody(request) {
		return hlsViewRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	return hlsViewRequest{service: service, slot: slot, key: key, operation: operation}, nil
}

// parseHLSSegmentRequestは固定segment pathからkeyと64 bit連番を取り出し、GETと空Cookieを確認する。
func parseHLSSegmentRequest(request *http.Request) (hlsSegmentRequest, *hlsHTTPRejection) {
	if request == nil {
		return hlsSegmentRequest{}, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	if request.Method != http.MethodGet {
		return hlsSegmentRequest{}, rejectHLS(http.StatusMethodNotAllowed, "method-not-allowed", http.MethodGet)
	}
	if request.URL == nil || request.URL.RawPath != "" {
		return hlsSegmentRequest{}, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		return hlsSegmentRequest{}, rejectHLS(http.StatusBadRequest, "invalid-query", "")
	}
	const prefix, suffix = "/komorebi/live/", ".ts"
	if !strings.HasPrefix(request.URL.Path, prefix) || !strings.HasSuffix(request.URL.Path, suffix) {
		return hlsSegmentRequest{}, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	remainder := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), suffix)
	key, rawSequence, found := strings.Cut(remainder, "/")
	if !found || strings.Contains(rawSequence, "/") || !validHLSKey(key) {
		return hlsSegmentRequest{}, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	sequence, err := strconv.ParseUint(rawSequence, 10, 64)
	if err != nil || strconv.FormatUint(sequence, 10) != rawSequence {
		return hlsSegmentRequest{}, rejectHLS(http.StatusNotFound, "not-found", "")
	}
	if !validEmptyHLSCookie(request) {
		return hlsSegmentRequest{}, rejectHLS(http.StatusBadRequest, "invalid-view-request", "")
	}
	return hlsSegmentRequest{key: key, sequence: sequence}, nil
}

// parseStrictHLSValuesはpercent decode前のfield名も検査し、encoded keyや空fieldを受けない。
func parseStrictHLSValues(raw string) (url.Values, bool) {
	if raw == "" {
		return nil, false
	}
	for _, field := range strings.Split(raw, "&") {
		name, _, found := strings.Cut(field, "=")
		if !found || name == "" || !plainHLSFieldName(name) {
			return nil, false
		}
	}
	values, err := url.ParseQuery(raw)
	return values, err == nil
}

// plainHLSFieldNameはqueryとformの名前を大小文字を保ったASCIIだけに限定する。
func plainHLSFieldName(value string) bool {
	for index := range len(value) {
		character := value[index]
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return false
		}
	}
	return value != ""
}

// oneHLSValueは同じ名前がちょうど一件だけある場合に値を返す。
func oneHLSValue(values url.Values, name string) (string, bool) {
	items, ok := values[name]
	if !ok || len(items) != 1 {
		return "", false
	}
	return items[0], true
}

// oneHLSServiceIDは先頭0のない1～65535の三要素だけを放送IDとして受ける。
func oneHLSServiceID(values url.Values, name string) (hlsServiceID, bool) {
	raw, ok := oneHLSValue(values, name)
	if !ok {
		return hlsServiceID{}, false
	}
	parts := strings.Split(raw, "-")
	if len(parts) != 3 {
		return hlsServiceID{}, false
	}
	onid, onidOK := parseHLSUint16(parts[0])
	tsid, tsidOK := parseHLSUint16(parts[1])
	sid, sidOK := parseHLSUint16(parts[2])
	return hlsServiceID{onid: onid, tsid: tsid, sid: sid}, onidOK && tsidOK && sidOK
}

// parseHLSUint16は0、符号、先頭0、範囲外を表記の比較で拒否する。
func parseHLSUint16(raw string) (uint16, bool) {
	value, err := strconv.ParseUint(raw, 10, 16)
	return uint16(value), err == nil && value > 0 && strconv.FormatUint(value, 10) == raw
}

// oneHLSSlotは主画面0または二画面目1を各一件だけ受ける。
func oneHLSSlot(values url.Values, name string) (uint8, bool) {
	raw, ok := oneHLSValue(values, name)
	if !ok || raw != "0" && raw != "1" {
		return 0, false
	}
	return raw[0] - '0', true
}

// validHLSViewFormは固定Content-Typeと、上限内のctok／open各一件だけを受ける。
func validHLSViewForm(request *http.Request) bool {
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 || contentTypes[0] != "application/x-www-form-urlencoded" {
		return false
	}
	values, ok := readHLSForm(request.Body, request.ContentLength)
	if !ok || len(values) != 2 {
		return false
	}
	token, tokenOK := oneHLSValue(values, "ctok")
	open, openOK := oneHLSValue(values, "open")
	return tokenOK && token == "" && openOK && open == "1"
}

// readHLSFormはencoded bodyも有界に読み、percent decode後の長さを1 KiB以下に保つ。
func readHLSForm(body io.Reader, contentLength int64) (url.Values, bool) {
	if body == nil || contentLength > hlsMaximumEncodedFormBytes || contentLength < -1 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(body, hlsMaximumEncodedFormBytes+1))
	if err != nil || len(raw) > hlsMaximumEncodedFormBytes || contentLength >= 0 && contentLength != int64(len(raw)) {
		return nil, false
	}
	decoded, err := url.QueryUnescape(string(raw))
	if err != nil || len(decoded) > hlsMaximumDecodedFormBytes {
		return nil, false
	}
	return parseStrictHLSValues(string(raw))
}

// validEmptyHLSCookieは非引用のctok=が全Cookie header内に一件だけあることを確認する。
func validEmptyHLSCookie(request *http.Request) bool {
	if request == nil {
		return false
	}
	count := 0
	for _, line := range request.Header.Values("Cookie") {
		cookies, err := http.ParseCookie(line)
		if err != nil {
			return false
		}
		for _, cookie := range cookies {
			if cookie.Name != "ctok" {
				continue
			}
			count++
			if cookie.Value != "" || cookie.Quoted {
				return false
			}
		}
	}
	return count == 1
}

// emptyHLSBodyはGET viewにContent-Length、chunked指定、実データのいずれもないことを確かめる。
func emptyHLSBody(request *http.Request) bool {
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		return false
	}
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	var buffer [1]byte
	read, err := request.Body.Read(buffer[:])
	return read == 0 && err == io.EOF
}

// rejectHLSは入力値を含めず、固定応答に必要な値だけをまとめる。
func rejectHLS(status int, reason, allow string) *hlsHTTPRejection {
	return &hlsHTTPRejection{status: status, reason: reason, allow: allow}
}
