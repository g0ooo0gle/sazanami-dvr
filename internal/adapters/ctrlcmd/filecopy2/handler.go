// Package filecopy2は、KonomiTVが必要とする固定設定と局ロゴをCtrlCmd 2060で返す。
// 固定設定は外部へ接続せず、局ロゴだけを検証済みの提供元へ問い合わせる。
package filecopy2

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

const (
	// CommandはKonomiTVが複数ファイルの取得に使うCtrlCmd番号である。
	Command int32 = 2060
	// Versionは対応するCmd2の通信版である。
	Version uint16 = 5

	resultSuccess     int32 = 1
	resultUnsupported int32 = 203

	maxRequestBody  = 512
	maxResponseBody = 8 * 1024 * 1024
	maxLogoBody     = 2 * 1024 * 1024
	maxLogoID       = 4_095
	maxLogoServices = 4_096
)

const (
	logoMapName   = "LogoData.ini"
	logoIndexName = `LogoData\*.*`
	logoPrefix    = `LogoData\`
)

var files = map[string][]byte{
	"Bitrate.ini": []byte("\xef\xbb\xbf[BITRATE]\r\n"),
	"EpgTimerSrv.ini": []byte("\xef\xbb\xbf[SET]\r\n" +
		"StartMargin=5\r\n" +
		"EndMargin=2\r\n" +
		"Caption=1\r\n" +
		"Data=0\r\n" +
		"RecEndMode=0\r\n" +
		"Reboot=0\r\n" +
		"PresetID=\r\n" +
		"\r\n" +
		"[REC_DEF]\r\n" +
		"SetName=デフォルト\r\n" +
		"RecMode=1\r\n" +
		"NoRecMode=1\r\n" +
		"Priority=3\r\n" +
		"TuijyuuFlag=1\r\n" +
		"ServiceMode=0\r\n" +
		"PittariFlag=0\r\n" +
		"BatFilePath=\r\n" +
		"SuspendMode=0\r\n" +
		"RebootFlag=0\r\n" +
		"UseMargineFlag=0\r\n" +
		"StartMargine=0\r\n" +
		"EndMargine=0\r\n" +
		"ContinueRec=0\r\n" +
		"PartialRec=0\r\n" +
		"TunerID=0\r\n"),
}

// LogoProviderは完成済みスナップショットで照合したMirakurunサービスのPNGだけを返す。
type LogoProvider interface {
	Logo(context.Context, provider.TuningTarget) ([]byte, error)
}

// LogoServiceは完成済み番組表からロゴ生成に必要な項目だけを受け取る。
type LogoService struct {
	ProviderLocator string
	NetworkID       uint16
	ServiceID       uint16
}

// LogoSourceは要求開始時に固定した選択済みサービスだけを返す。
type LogoSource interface {
	CurrentLogos(context.Context) ([]LogoService, error)
}

// Handlerは要求全体を検証してから、許可した固定データまたは局ロゴだけを返す。
// 応答サイズを事前に確定し、書き込み開始後に別の応答へ切り替えない。
type Handler struct {
	Source LogoSource
	Logos  LogoProvider
	Limits codec.Limits
}

// Handleはコマンド2060の1要求を処理する。
func (handler Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	limits := handler.commandLimits()
	frame, err := codec.ParseRequestFrame(request, limits)
	if err != nil {
		return err
	}
	if frame.Code != Command {
		return failure(codec.Unsupported, "command-out-of-profile", int64(frame.Code))
	}
	if ctx == nil {
		return failure(codec.Internal, "nil-context", 0)
	}
	if ctx.Err() != nil {
		return failure(codec.Timeout, "request-context-ended", 0)
	}

	reader, err := codec.NewReader(frame.Body, limits)
	if err != nil {
		return err
	}
	version, err := reader.U16()
	if err != nil {
		return err
	}
	names := make([]string, 0, 2)
	if err := reader.Vector(6, 2, func(item *codec.Reader, _ int) error {
		value, readErr := item.String()
		if readErr != nil {
			return readErr
		}
		names = append(names, value)
		return nil
	}); err != nil {
		return err
	}
	if err := reader.Exact(); err != nil {
		return err
	}
	if len(names) < 1 || len(names) > 2 {
		return failure(codec.Malformed, "file-name-count", int64(len(names)))
	}
	if destination == nil {
		return failure(codec.Internal, "nil-response-writer", 0)
	}
	if version != Version {
		return writeUnsupported(ctx, destination, limits)
	}
	if len(names) == 1 {
		if data, ok := files[names[0]]; ok {
			return writeFile(ctx, destination, limits, names[0], data)
		}
		if strings.HasPrefix(names[0], logoPrefix) {
			return handler.writeLogo(ctx, destination, limits, names[0])
		}
		return writeUnsupported(ctx, destination, limits)
	}
	if names[0] != logoMapName || names[1] != logoIndexName {
		return writeUnsupported(ctx, destination, limits)
	}
	entries, err := projectLogos(ctx, handler.Source)
	if err != nil {
		return err
	}
	return writeFiles(ctx, destination, limits, logoIndexFiles(entries))
}

func (handler Handler) commandLimits() codec.Limits {
	limits := handler.Limits
	if limits.RequestBody == 0 || limits.RequestBody > maxRequestBody {
		limits.RequestBody = maxRequestBody
	}
	if limits.ResponseBody == 0 || limits.ResponseBody > maxResponseBody {
		limits.ResponseBody = maxResponseBody
	}
	return limits
}

func writeUnsupported(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(contextDestination{ctx: ctx, destination: destination}, resultUnsupported, 0, limits, func(*codec.Writer) error { return nil })
}

func writeFile(ctx context.Context, destination io.Writer, limits codec.Limits, name string, data []byte) error {
	return writeFiles(ctx, destination, limits, []responseFile{{name: name, data: data}})
}

type responseFile struct {
	name   string
	data   []byte
	extent int64
}

func writeFiles(ctx context.Context, destination io.Writer, limits codec.Limits, files []responseFile) error {
	if len(files) < 1 || len(files) > 2 {
		return failure(codec.Internal, "file-response-count", int64(len(files)))
	}
	vectorExtent := int64(8)
	for index := range files {
		nameExtent, err := codec.StringSize(files[index].name, limits)
		if err != nil {
			return err
		}
		files[index].extent = int64(4) + nameExtent + 4 + 4 + int64(len(files[index].data))
		if files[index].extent > 1<<31-1 || len(files[index].data) > 1<<31-1 {
			return failure(codec.OverLimit, "file-response-size", files[index].extent)
		}
		vectorExtent += files[index].extent
	}
	bodySize := int64(2) + vectorExtent
	if vectorExtent > 1<<31-1 {
		return failure(codec.OverLimit, "file-response-size", bodySize)
	}
	return codec.WriteFrame(contextDestination{ctx: ctx, destination: destination}, resultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		if err := writer.I32(int32(vectorExtent)); err != nil {
			return err
		}
		if err := writer.I32(int32(len(files))); err != nil {
			return err
		}
		for _, file := range files {
			if err := writer.I32(int32(file.extent)); err != nil {
				return err
			}
			if err := writer.String(file.name); err != nil {
				return err
			}
			if err := writer.I32(int32(len(file.data))); err != nil {
				return err
			}
			if err := writer.I32(0); err != nil {
				return err
			}
			if err := writer.Bytes(file.data); err != nil {
				return err
			}
		}
		return nil
	})
}

type logoEntry struct {
	name    string
	locator string
	network uint16
	service uint16
}

func projectLogos(ctx context.Context, source LogoSource) ([]logoEntry, error) {
	if source == nil {
		return nil, failure(codec.Internal, "missing-logo-source", 0)
	}
	services, err := source.CurrentLogos(ctx)
	if err != nil {
		return nil, failure(codec.Internal, "logo-snapshot-unavailable", 0)
	}
	if len(services) > maxLogoServices {
		return nil, failure(codec.Internal, "logo-snapshot-invalid", int64(len(services)))
	}
	type identity struct{ network, service uint16 }
	counts := make(map[identity]int, len(services))
	for _, service := range services {
		counts[identity{service.NetworkID, service.ServiceID}]++
	}
	entries := make([]logoEntry, 0, len(services))
	for _, service := range services {
		if ctx.Err() != nil {
			return nil, failure(codec.Timeout, "request-context-ended", 0)
		}
		key := identity{service.NetworkID, service.ServiceID}
		if counts[key] != 1 || service.ServiceID > maxLogoID || !canonicalLocator(service.ProviderLocator) {
			continue
		}
		entries = append(entries, logoEntry{
			name:    fmt.Sprintf("%04X_%03X_000_05.png", service.NetworkID, service.ServiceID),
			locator: service.ProviderLocator, network: service.NetworkID, service: service.ServiceID,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].network != entries[j].network {
			return entries[i].network < entries[j].network
		}
		if entries[i].service != entries[j].service {
			return entries[i].service < entries[j].service
		}
		return entries[i].locator < entries[j].locator
	})
	return entries, nil
}

func canonicalLocator(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 63)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func logoIndexFiles(entries []logoEntry) []responseFile {
	var mapping, index bytes.Buffer
	mapping.Write([]byte{0xef, 0xbb, 0xbf})
	for _, entry := range entries {
		mapping.WriteString(fmt.Sprintf("%04X%04X=%d\r\n", entry.network, entry.service, entry.service))
		index.WriteString("0 0 0 ")
		index.WriteString(entry.name)
		index.WriteString("\r\n")
	}
	return []responseFile{{name: logoMapName, data: mapping.Bytes()}, {name: logoIndexName, data: index.Bytes()}}
}

func (handler Handler) writeLogo(ctx context.Context, destination io.Writer, limits codec.Limits, requested string) error {
	entries, err := projectLogos(ctx, handler.Source)
	if err != nil {
		return err
	}
	base := strings.TrimPrefix(requested, logoPrefix)
	var selected *logoEntry
	for index := range entries {
		if entries[index].name == base {
			if selected != nil {
				return writeUnsupported(ctx, destination, limits)
			}
			selected = &entries[index]
		}
	}
	if selected == nil || handler.Logos == nil {
		return writeUnsupported(ctx, destination, limits)
	}
	target, err := provider.NewTuningTarget(selected.locator)
	if err != nil {
		return writeUnsupported(ctx, destination, limits)
	}
	data, err := handler.Logos.Logo(ctx, target)
	if err != nil {
		if ctx.Err() != nil {
			return failure(codec.Timeout, "request-context-ended", 0)
		}
		return writeUnsupported(ctx, destination, limits)
	}
	if len(data) < 1 || len(data) > maxLogoBody || !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		return writeUnsupported(ctx, destination, limits)
	}
	return writeFile(ctx, destination, limits, requested, data)
}

type contextDestination struct {
	ctx         context.Context
	destination io.Writer
}

// Writeは応答の各書き込み前に取り消しを確認し、接続エラーを安定分類へ変換する。
func (destination contextDestination) Write(value []byte) (int, error) {
	if err := destination.ctx.Err(); err != nil {
		return 0, failure(codec.Timeout, "request-context-ended", 0)
	}
	n, err := destination.destination.Write(value)
	if err != nil {
		return n, failure(codec.PeerDisconnect, "response-write-failed", int64(n))
	}
	return n, nil
}

func failure(category codec.Category, reason string, size int64) error {
	return &codec.Error{Category: category, Reason: reason, Size: size}
}
