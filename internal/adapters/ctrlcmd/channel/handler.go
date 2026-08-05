// Package channelは、検証済みのチャンネル一覧をCtrlCmd 1060／1021の応答へ変換する。
// このパッケージ自体はDB、プロバイダー、ファイルシステム、リスナーへ接続しない。
package channel

import (
	"context"
	"io"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
)

const (
	CommandFileCopy    int32 = 1060
	CommandEnumService int32 = 1021
	ResultSuccess      int32 = 1
	ResultUnsupported  int32 = 203

	fileName            = "ChSet5.txt"
	fileRequestBodySize = 26
	maxServices         = 4_096
	maxFileBody         = 8 * 1024 * 1024
	maxServiceBody      = 16 * 1024 * 1024
)

// Handlerは、厳密に検証した1060／1021リクエストを、1つのスナップショットから導出した応答へ変換する。
// Sourceの取得、一覧の検証、応答サイズの確定がすべて終わるまで書き込みを始めない。
type Handler struct {
	Source Source
	Limits codec.Limits
}

// Handleは1件のリクエストを処理する。正しいリクエストでもSourceの参照は最大1回に限る。
func (h Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	frame, err := codec.ParseRequestFrame(request, h.Limits)
	if err != nil {
		return err
	}
	if ctx == nil {
		return codecFailure(codec.Internal, "nil-context", 0)
	}

	switch frame.Code {
	case CommandFileCopy:
		if len(frame.Body) != fileRequestBodySize {
			return codecFailure(codec.Malformed, "file-request-body-size", int64(len(frame.Body)))
		}
		reader, readErr := codec.NewReader(frame.Body, h.Limits)
		if readErr != nil {
			return readErr
		}
		requested, readErr := reader.String()
		if readErr != nil {
			return readErr
		}
		if readErr := reader.Exact(); readErr != nil {
			return readErr
		}
		if requested != fileName {
			if destination == nil {
				return codecFailure(codec.Internal, "nil-response-writer", 0)
			}
			return writeUnsupported(ctx, destination, h.commandLimits(maxFileBody))
		}
	case CommandEnumService:
		if len(frame.Body) != 0 {
			return codecFailure(codec.Malformed, "enum-request-body-size", int64(len(frame.Body)))
		}
	default:
		return codecFailure(codec.Unsupported, "command-out-of-profile", int64(frame.Code))
	}
	if destination == nil {
		return codecFailure(codec.Internal, "nil-response-writer", 0)
	}

	services, err := project(ctx, h.Source)
	if err != nil {
		return err
	}
	if frame.Code == CommandFileCopy {
		return writeFile(ctx, destination, services, h.commandLimits(maxFileBody))
	}
	return writeServiceVector(ctx, destination, services, h.commandLimits(maxServiceBody))
}

func (h Handler) commandLimits(responseCap int) codec.Limits {
	limits := h.Limits
	if limits.ResponseBody == 0 || limits.ResponseBody > responseCap {
		limits.ResponseBody = responseCap
	}
	if limits.VectorElements == 0 || limits.VectorElements > maxServices {
		limits.VectorElements = maxServices
	}
	return limits
}

func writeUnsupported(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(contextWriter{ctx: ctx, w: destination}, ResultUnsupported, 0, limits, func(*codec.Writer) error { return nil })
}

type contextWriter struct {
	ctx context.Context
	w   io.Writer
}

// Writeは、書き込みの直前にリクエストのcontextが有効か確認する。
func (w contextWriter) Write(value []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, codecFailure(codec.Timeout, "request-context-ended", 0)
	}
	n, err := w.w.Write(value)
	if err != nil {
		return n, codecFailure(codec.PeerDisconnect, "response-write-failed", int64(n))
	}
	return n, nil
}

func codecFailure(category codec.Category, reason string, size int64) error {
	return &codec.Error{Category: category, Reason: reason, Size: size}
}
