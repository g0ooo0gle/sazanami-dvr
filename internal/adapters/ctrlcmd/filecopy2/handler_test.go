package filecopy2

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

var testLogoPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}

func TestHandlerReturnsAllowedFixedFiles(t *testing.T) {
	source := &logoSource{}
	logos := &logoProvider{data: testLogoPNG}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "Bitrate.ini", data: []byte("\xef\xbb\xbf[BITRATE]\r\n")},
		{name: "EpgTimerSrv.ini", data: []byte("\xef\xbb\xbf[SET]\r\nStartMargin=5\r\nEndMargin=2\r\nCaption=1\r\nData=0\r\nRecEndMode=0\r\nReboot=0\r\nPresetID=\r\n\r\n[REC_DEF]\r\nSetName=デフォルト\r\nRecMode=1\r\nNoRecMode=1\r\nPriority=3\r\nTuijyuuFlag=1\r\nServiceMode=0\r\nPittariFlag=0\r\nBatFilePath=\r\nSuspendMode=0\r\nRebootFlag=0\r\nUseMargineFlag=0\r\nStartMargine=0\r\nEndMargine=0\r\nContinueRec=0\r\nPartialRec=0\r\nTunerID=0\r\n")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response bytes.Buffer
			if err := (Handler{Source: source, Logos: logos}).Handle(context.Background(), request(t, Version, test.name), &response); err != nil {
				t.Fatal(err)
			}
			name, data := decodeResponse(t, response.Bytes())
			if name != test.name || !bytes.Equal(data, test.data) {
				t.Fatalf("name=%q data=%q", name, data)
			}
			if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !bytes.HasSuffix(data, []byte("\r\n")) {
				t.Fatalf("BOMまたは末尾CRLFがありません: %x", data)
			}
			if bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte{'\n'}) {
				t.Fatal("CRLF以外の改行が含まれています")
			}
		})
	}
	if source.calls.Load() != 0 || logos.calls.Load() != 0 {
		t.Fatalf("固定ファイル取得が外部データを参照しました: source=%d logo=%d", source.calls.Load(), logos.calls.Load())
	}
}

func TestHandlerReturnsLogoIndexAndPNG(t *testing.T) {
	source := &logoSource{services: []LogoService{logoService(1, 2, 3, "100002")}}
	logos := &logoProvider{data: testLogoPNG}
	handler := Handler{Source: source, Logos: logos}

	var indexResponse bytes.Buffer
	if err := handler.Handle(context.Background(), requestNames(t, Version, []string{logoMapName, logoIndexName}), &indexResponse); err != nil {
		t.Fatal(err)
	}
	indexFiles := decodeResponses(t, indexResponse.Bytes())
	if len(indexFiles) != 2 || indexFiles[0].name != logoMapName || !bytes.Equal(indexFiles[0].data, []byte("\xef\xbb\xbf00010002=2\r\n")) {
		t.Fatalf("mapping=%+v", indexFiles)
	}
	if indexFiles[1].name != logoIndexName || string(indexFiles[1].data) != "0 0 0 0001_002_000_05.png\r\n" {
		t.Fatalf("index=%+v", indexFiles[1])
	}
	if logos.calls.Load() != 0 {
		t.Fatalf("一覧生成時に画像を取得しました: %d", logos.calls.Load())
	}

	var logoResponse bytes.Buffer
	name := `LogoData\0001_002_000_05.png`
	if err := handler.Handle(context.Background(), request(t, Version, name), &logoResponse); err != nil {
		t.Fatal(err)
	}
	responseName, data := decodeResponse(t, logoResponse.Bytes())
	if responseName != name || !bytes.Equal(data, testLogoPNG) {
		t.Fatalf("name=%q data=%x", responseName, data)
	}
	if logos.calls.Load() != 1 || logos.target != "100002" {
		t.Fatalf("logo calls=%d target=%q", logos.calls.Load(), logos.target)
	}
}

func TestHandlerReturnsEmptyLogoIndex(t *testing.T) {
	var response bytes.Buffer
	handler := Handler{Source: &logoSource{}}
	if err := handler.Handle(context.Background(), requestNames(t, Version, []string{logoMapName, logoIndexName}), &response); err != nil {
		t.Fatal(err)
	}
	files := decodeResponses(t, response.Bytes())
	if len(files) != 2 || !bytes.Equal(files[0].data, []byte{0xef, 0xbb, 0xbf}) || len(files[1].data) != 0 {
		t.Fatalf("files=%+v", files)
	}
}

func TestHandlerFiltersAmbiguousAndUnrepresentableLogos(t *testing.T) {
	services := []LogoService{
		logoService(1, 2, 3, "100002"),
		logoService(1, 2, 4, "100003"),
		logoService(2, 4_096, 5, "100004"),
		logoService(3, 3, 6, "0100005"),
	}
	source := &logoSource{services: services}
	var response bytes.Buffer
	if err := (Handler{Source: source}).Handle(context.Background(), requestNames(t, Version, []string{logoMapName, logoIndexName}), &response); err != nil {
		t.Fatal(err)
	}
	files := decodeResponses(t, response.Bytes())
	if len(files) != 2 || !bytes.Equal(files[0].data, []byte{0xef, 0xbb, 0xbf}) || len(files[1].data) != 0 {
		t.Fatalf("除外結果=%+v", files)
	}
}

func TestHandlerAppliesLogoServiceCountCap(t *testing.T) {
	services := make([]LogoService, maxLogoServices)
	for index := range services {
		services[index] = logoService(uint16(index+1), uint16(index), 1, strconv.Itoa(index+1))
	}
	var exact bytes.Buffer
	if err := (Handler{Source: &logoSource{services: services}}).Handle(context.Background(), requestNames(t, Version, []string{logoMapName, logoIndexName}), &exact); err != nil {
		t.Fatal(err)
	}
	if files := decodeResponses(t, exact.Bytes()); len(files) != 2 {
		t.Fatalf("files=%d", len(files))
	}

	over := append(services, logoService(5_000, 1, 1, "5000001"))
	err := (Handler{Source: &logoSource{services: over}}).Handle(context.Background(), requestNames(t, Version, []string{logoMapName, logoIndexName}), io.Discard)
	assertCategory(t, err, codec.Internal)
}

func TestHandlerRejectsUnsupportedLogoRequestsWithoutFetching(t *testing.T) {
	source := &logoSource{services: []LogoService{logoService(1, 2, 3, "100002")}}
	tests := [][]string{
		{logoIndexName, logoMapName},
		{logoMapName, logoMapName},
		{logoMapName},
		{logoIndexName},
		{`LogoData\0001_003_000_05.png`},
		{`LogoData\../0001_002_000_05.png`},
	}
	for _, names := range tests {
		var response bytes.Buffer
		logos := &logoProvider{data: testLogoPNG}
		if err := (Handler{Source: source, Logos: logos}).Handle(context.Background(), requestNames(t, Version, names), &response); err != nil {
			t.Fatalf("names=%q: %v", names, err)
		}
		assertUnsupported(t, response.Bytes())
		if logos.calls.Load() != 0 {
			t.Fatalf("不正な名前で画像を取得しました: names=%q", names)
		}
	}
}

func TestHandlerRejectsFailedOrInvalidLogo(t *testing.T) {
	source := &logoSource{services: []LogoService{logoService(1, 2, 3, "100002")}}
	name := `LogoData\0001_002_000_05.png`
	tests := []LogoProvider{
		nil,
		&logoProvider{err: errors.New("private provider detail")},
		&logoProvider{data: nil},
		&logoProvider{data: []byte("not png")},
		&logoProvider{data: append(testLogoPNG, make([]byte, maxLogoBody)...)},
	}
	for index, logos := range tests {
		var response bytes.Buffer
		if err := (Handler{Source: source, Logos: logos}).Handle(context.Background(), request(t, Version, name), &response); err != nil {
			t.Fatalf("case=%d: %v", index, err)
		}
		assertUnsupported(t, response.Bytes())
	}
}

func TestHandlerRejectsNamesAndVersionsWithoutReadingFiles(t *testing.T) {
	names := []string{"", "bitrate.ini", "../Bitrate.ini", "/Bitrate.ini", `dir\\Bitrate.ini`, "*.ini"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var response bytes.Buffer
			if err := (Handler{}).Handle(context.Background(), request(t, Version, name), &response); err != nil {
				t.Fatal(err)
			}
			assertUnsupported(t, response.Bytes())
		})
	}
	for _, version := range []uint16{4, 6} {
		var response bytes.Buffer
		if err := (Handler{}).Handle(context.Background(), request(t, version, "Bitrate.ini"), &response); err != nil {
			t.Fatal(err)
		}
		assertUnsupported(t, response.Bytes())
	}
}

func TestHandlerRejectsMalformedAndOverLimitRequests(t *testing.T) {
	valid := request(t, Version, "Bitrate.ini")
	tests := []struct {
		name     string
		request  []byte
		category codec.Category
	}{
		{name: "途中切断", request: valid[:len(valid)-1], category: codec.Truncated},
		{name: "余剰", request: appendWithDeclaredBody(valid, 0), category: codec.Malformed},
		{name: "壊れた配列extent", request: replaceI32(valid, codec.HeaderSize+2, 7), category: codec.Malformed},
		{name: "負の配列extent", request: replaceI32(valid, codec.HeaderSize+2, -1), category: codec.Malformed},
		{name: "空配列", request: vectorRequest(t, 0), category: codec.Malformed},
		{name: "3件配列", request: vectorRequest(t, 3), category: codec.OverLimit},
		{name: "文字列内NUL", request: embeddedNULRequest(), category: codec.Malformed},
		{name: "本文511", request: sizedRequest(511), category: codec.Malformed},
		{name: "本文512", request: sizedRequest(512), category: codec.Malformed},
		{name: "本文513", request: sizedRequest(513), category: codec.OverLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var response bytes.Buffer
			err := (Handler{}).Handle(context.Background(), test.request, &response)
			assertCategory(t, err, test.category)
			if response.Len() != 0 {
				t.Fatalf("不正入力へ応答しました: %x", response.Bytes())
			}
		})
	}

	badCommand := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(badCommand[0:4], 2061)
	assertCategory(t, (Handler{}).Handle(context.Background(), badCommand, io.Discard), codec.Unsupported)
}

func TestHandlerHandlesContextAndWriterFailures(t *testing.T) {
	valid := request(t, Version, "Bitrate.ini")
	var response bytes.Buffer
	assertCategory(t, (Handler{}).Handle(nil, valid, &response), codec.Internal)
	assertCategory(t, (Handler{}).Handle(context.Background(), valid, nil), codec.Internal)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assertCategory(t, (Handler{}).Handle(canceled, valid, &response), codec.Timeout)
	if response.Len() != 0 {
		t.Fatalf("取り消し後に応答しました: %x", response.Bytes())
	}

	failing := &failWriter{limit: 9}
	assertCategory(t, (Handler{}).Handle(context.Background(), valid, failing), codec.PeerDisconnect)
	if failing.written > failing.limit {
		t.Fatalf("失敗後も書き込みました: %d", failing.written)
	}

	var short bytes.Buffer
	if err := (Handler{}).Handle(context.Background(), valid, oneByteWriter{destination: &short}); err != nil {
		t.Fatal(err)
	}
	decodeResponse(t, short.Bytes())

	assertCategory(t, (Handler{}).Handle(context.Background(), valid, zeroWriter{}), codec.PeerDisconnect)
}

func TestHandlerAppliesCommandSpecificCaps(t *testing.T) {
	valid := request(t, Version, "Bitrate.ini")
	assertCategory(t, (Handler{Limits: codec.Limits{RequestBody: len(valid) - codec.HeaderSize - 1}}).Handle(context.Background(), valid, io.Discard), codec.OverLimit)
	assertCategory(t, (Handler{Limits: codec.Limits{ResponseBody: 1}}).Handle(context.Background(), valid, io.Discard), codec.OverLimit)
}

func request(t *testing.T, version uint16, name string) []byte {
	return requestNames(t, version, []string{name})
}

func requestNames(t *testing.T, version uint16, names []string) []byte {
	t.Helper()
	limits := codec.DefaultLimits()
	vectorExtent := int64(8)
	for _, name := range names {
		nameExtent, err := codec.StringSize(name, limits)
		if err != nil {
			t.Fatal(err)
		}
		vectorExtent += nameExtent
	}
	var body bytes.Buffer
	writer, err := codec.NewWriter(&body, limits)
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []func() error{
		func() error { return writer.U16(version) },
		func() error { return writer.I32(int32(vectorExtent)) },
		func() error { return writer.I32(int32(len(names))) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range names {
		if err := writer.String(name); err != nil {
			t.Fatal(err)
		}
	}
	return frame(Command, body.Bytes())
}

func vectorRequest(t *testing.T, count int) []byte {
	t.Helper()
	limits := codec.DefaultLimits()
	nameExtent, err := codec.StringSize("Bitrate.ini", limits)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer, err := codec.NewWriter(&body, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.U16(Version); err != nil {
		t.Fatal(err)
	}
	if err := writer.I32(int32(8 + int64(count)*nameExtent)); err != nil {
		t.Fatal(err)
	}
	if err := writer.I32(int32(count)); err != nil {
		t.Fatal(err)
	}
	for range count {
		if err := writer.String("Bitrate.ini"); err != nil {
			t.Fatal(err)
		}
	}
	return frame(Command, body.Bytes())
}

func decodeResponse(t *testing.T, value []byte) (string, []byte) {
	t.Helper()
	files := decodeResponses(t, value)
	if len(files) != 1 {
		t.Fatalf("files=%d", len(files))
	}
	return files[0].name, files[0].data
}

type decodedFile struct {
	name string
	data []byte
}

func decodeResponses(t *testing.T, value []byte) []decodedFile {
	t.Helper()
	response, err := codec.ParseRequestFrame(value, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != resultSuccess {
		t.Fatalf("result=%d", response.Code)
	}
	reader, err := codec.NewReader(response.Body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	version, err := reader.U16()
	if err != nil || version != Version {
		t.Fatalf("version=%d err=%v", version, err)
	}
	files := make([]decodedFile, 0, 2)
	if err := reader.Vector(4, 2, func(item *codec.Reader, _ int) error {
		return item.Structure(func(record *codec.Reader) error {
			name, readErr := record.String()
			if readErr != nil {
				return readErr
			}
			length, readErr := record.I32()
			if readErr != nil || length < 0 {
				return readErr
			}
			reserved, readErr := record.I32()
			if readErr != nil || reserved != 0 {
				return readErr
			}
			data := make([]byte, int(length))
			for index := range data {
				data[index], readErr = record.U8()
				if readErr != nil {
					return readErr
				}
			}
			files = append(files, decodedFile{name: name, data: data})
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil {
		t.Fatal(err)
	}
	return files
}

func logoService(network, service, _ uint16, locator string) LogoService {
	return LogoService{ProviderLocator: locator, NetworkID: network, ServiceID: service}
}

type logoSource struct {
	services []LogoService
	err      error
	calls    atomic.Int32
}

func (source *logoSource) CurrentLogos(context.Context) ([]LogoService, error) {
	source.calls.Add(1)
	return source.services, source.err
}

type logoProvider struct {
	data   []byte
	err    error
	target string
	calls  atomic.Int32
}

func (logos *logoProvider) Logo(_ context.Context, target provider.TuningTarget) ([]byte, error) {
	logos.calls.Add(1)
	logos.target = target.Opaque
	return logos.data, logos.err
}

func assertUnsupported(t *testing.T, value []byte) {
	t.Helper()
	response, err := codec.ParseRequestFrame(value, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != resultUnsupported || len(response.Body) != 0 {
		t.Fatalf("response=%x", value)
	}
}

func assertCategory(t *testing.T, err error, want codec.Category) {
	t.Helper()
	var codecError *codec.Error
	if !errors.As(err, &codecError) || codecError.Category != want {
		t.Fatalf("error=%v category=%v want=%v", err, codecError, want)
	}
}

func frame(command int32, body []byte) []byte {
	value := make([]byte, codec.HeaderSize+len(body))
	binary.LittleEndian.PutUint32(value[0:4], uint32(command))
	binary.LittleEndian.PutUint32(value[4:8], uint32(len(body)))
	copy(value[codec.HeaderSize:], body)
	return value
}

func sizedRequest(size int) []byte { return frame(Command, make([]byte, size)) }

func appendWithDeclaredBody(value []byte, extra byte) []byte {
	copyValue := append(append([]byte(nil), value...), extra)
	binary.LittleEndian.PutUint32(copyValue[4:8], uint32(len(copyValue)-codec.HeaderSize))
	return copyValue
}

func replaceI32(value []byte, offset int, replacement int32) []byte {
	copyValue := append([]byte(nil), value...)
	binary.LittleEndian.PutUint32(copyValue[offset:offset+4], uint32(replacement))
	return copyValue
}

func embeddedNULRequest() []byte {
	body := make([]byte, 22)
	binary.LittleEndian.PutUint16(body[0:2], Version)
	binary.LittleEndian.PutUint32(body[2:6], 20)
	binary.LittleEndian.PutUint32(body[6:10], 1)
	binary.LittleEndian.PutUint32(body[10:14], 12)
	binary.LittleEndian.PutUint16(body[14:16], 'A')
	binary.LittleEndian.PutUint16(body[18:20], 'B')
	return frame(Command, body)
}

type failWriter struct {
	limit   int
	written int
}

func (writer *failWriter) Write(value []byte) (int, error) {
	remaining := writer.limit - writer.written
	if remaining <= 0 {
		return 0, errors.New("closed")
	}
	if len(value) > remaining {
		value = value[:remaining]
	}
	writer.written += len(value)
	return len(value), errors.New("closed")
}

type oneByteWriter struct{ destination io.Writer }

func (writer oneByteWriter) Write(value []byte) (int, error) {
	if len(value) > 1 {
		value = value[:1]
	}
	return writer.destination.Write(value)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
