// Package filecopy2は、KonomiTVが必要とする固定設定をCtrlCmd 2060で返す。
// このパッケージはDB、実ファイル、環境変数、ネットワークへ接続しない。
package filecopy2

import (
	"context"
	"io"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
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

// Handlerは要求全体を検証してから、許可した固定データを1件だけ返す。
// 応答サイズを事前に確定し、書き込み開始後に別の応答へ切り替えない。
type Handler struct {
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
	var name string
	count := 0
	if err := reader.Vector(6, 1, func(item *codec.Reader, _ int) error {
		value, readErr := item.String()
		if readErr != nil {
			return readErr
		}
		name = value
		count++
		return nil
	}); err != nil {
		return err
	}
	if err := reader.Exact(); err != nil {
		return err
	}
	if count != 1 {
		return failure(codec.Malformed, "file-name-count", int64(count))
	}
	if destination == nil {
		return failure(codec.Internal, "nil-response-writer", 0)
	}
	if version != Version {
		return writeUnsupported(ctx, destination, limits)
	}
	data, ok := files[name]
	if !ok {
		return writeUnsupported(ctx, destination, limits)
	}
	return writeFile(ctx, destination, limits, name, data)
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
	nameExtent, err := codec.StringSize(name, limits)
	if err != nil {
		return err
	}
	structureExtent := int64(4) + nameExtent + 4 + 4 + int64(len(data))
	vectorExtent := int64(8) + structureExtent
	bodySize := int64(2) + vectorExtent
	if structureExtent > 1<<31-1 || vectorExtent > 1<<31-1 || len(data) > 1<<31-1 {
		return failure(codec.OverLimit, "file-response-size", bodySize)
	}
	return codec.WriteFrame(contextDestination{ctx: ctx, destination: destination}, resultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		if err := writer.I32(int32(vectorExtent)); err != nil {
			return err
		}
		if err := writer.I32(1); err != nil {
			return err
		}
		if err := writer.I32(int32(structureExtent)); err != nil {
			return err
		}
		if err := writer.String(name); err != nil {
			return err
		}
		if err := writer.I32(int32(len(data))); err != nil {
			return err
		}
		if err := writer.I32(0); err != nil {
			return err
		}
		return writer.Bytes(data)
	})
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
