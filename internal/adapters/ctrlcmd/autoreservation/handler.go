// Package autoreservationは自動予約条件をKonomiTV向けCtrlCmd形式へ変換する。
package autoreservation

import (
	"context"
	"io"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
)

const (
	// CommandListは自動予約条件の一覧を返すCmd2番号である。
	CommandList int32 = 2131
	// CommandAddは自動予約条件を追加するCmd2番号である。
	CommandAdd int32 = 2132
	// CommandChangeは自動予約条件を変更するCmd2番号である。
	CommandChange int32 = 2134
	// CommandDeleteは自動予約条件を削除する従来形式の番号である。
	CommandDelete int32 = 1033
	// Versionは固定版KonomiTVとKomorebiが使うCmd2版である。
	Version       uint16 = 5
	resultSuccess int32  = 1
	resultFailure int32  = 0
)

// Operationsは自動予約条件の永続化操作を提供する。
type Operations interface {
	Add(context.Context, autoreservation.SearchCondition, autoreservation.RecordingSettings) (autoreservation.Rule, error)
	List(context.Context, int, int32) ([]autoreservation.Rule, error)
	Change(context.Context, int32, autoreservation.SearchCondition, autoreservation.RecordingSettings) error
	Delete(context.Context, int32) error
}

// Handlerは対応する4commandだけをapplication層へ渡す。
type Handler struct {
	Operations Operations
	Limits     codec.Limits
}

// Handleは外側frameを検証し、失敗時に内部情報を応答へ含めない。
func (handler Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	frame, err := codec.ParseRequestFrame(request, handler.Limits)
	if err != nil {
		return err
	}
	if ctx == nil || destination == nil || handler.Operations == nil {
		return failure(codec.Internal, "missing-automatic-reservation-dependency", 0)
	}
	switch frame.Code {
	case CommandList:
		return handler.list(ctx, frame.Body, destination)
	case CommandAdd:
		return handler.add(ctx, frame.Body, destination)
	case CommandChange:
		return handler.change(ctx, frame.Body, destination)
	case CommandDelete:
		return handler.delete(ctx, frame.Body, destination)
	default:
		return failure(codec.Unsupported, "command-out-of-profile", int64(frame.Code))
	}
}

func (handler Handler) list(ctx context.Context, body []byte, destination io.Writer) error {
	reader, err := codec.NewReader(body, handler.Limits)
	if err != nil {
		return err
	}
	version, err := reader.U16()
	if err != nil || version != Version || reader.Exact() != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	rules, err := handler.allRules(ctx)
	if err != nil {
		return err
	}
	responseLimit := handler.Limits.ResponseBody
	if responseLimit == 0 {
		responseLimit = codec.DefaultLimits().ResponseBody
	}
	vectorSize := int64(8)
	for _, rule := range rules {
		size, sizeErr := autoAddSize(rule, handler.Limits)
		if sizeErr != nil || size > int64(responseLimit)-vectorSize {
			return failure(codec.OverLimit, "automatic-rule-response", size)
		}
		vectorSize += size
	}
	bodySize := vectorSize + 2
	return codec.WriteFrame(responseDestination{ctx, destination}, resultSuccess, bodySize, handler.Limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		if err := writer.I32(int32(vectorSize)); err != nil {
			return err
		}
		if err := writer.I32(int32(len(rules))); err != nil {
			return err
		}
		for _, rule := range rules {
			if err := writeAutoAdd(writer, rule, handler.Limits); err != nil {
				return err
			}
		}
		return nil
	})
}

func (handler Handler) add(ctx context.Context, body []byte, destination io.Writer) error {
	rule, err := decodeOneRule(body, handler.Limits, false)
	if err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	if _, err := handler.Operations.Add(ctx, rule.Search, rule.Recording); err != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return writeVersionSuccess(ctx, destination, handler.Limits)
}

func (handler Handler) change(ctx context.Context, body []byte, destination io.Writer) error {
	rule, err := decodeOneRule(body, handler.Limits, true)
	if err != nil || handler.Operations.Change(ctx, rule.Number, rule.Search, rule.Recording) != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return writeVersionSuccess(ctx, destination, handler.Limits)
}

func (handler Handler) delete(ctx context.Context, body []byte, destination io.Writer) error {
	reader, err := codec.NewReader(body, handler.Limits)
	if err != nil {
		return err
	}
	count := 0
	var number int32
	err = reader.Vector(4, 1, func(item *codec.Reader, _ int) error {
		count++
		value, readErr := item.I32()
		number = value
		return readErr
	})
	if err != nil || count != 1 || number < 1 || reader.Exact() != nil || handler.Operations.Delete(ctx, number) != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	return codec.WriteFrame(responseDestination{ctx, destination}, resultSuccess, 0, handler.Limits, func(*codec.Writer) error { return nil })
}

func (handler Handler) allRules(ctx context.Context) ([]autoreservation.Rule, error) {
	rules := make([]autoreservation.Rule, 0, autoreservation.MaxRules)
	var after int32
	for {
		if err := ctx.Err(); err != nil {
			return nil, failure(codec.Timeout, "request-context-ended", 0)
		}
		page, err := handler.Operations.List(ctx, autoreservation.MaxPage, after)
		if err != nil || len(page) > autoreservation.MaxPage {
			return nil, failure(codec.Internal, "automatic-rule-source-failed", int64(len(page)))
		}
		for _, rule := range page {
			if rule.Number <= after || len(rules) == autoreservation.MaxRules || rule.ValidateStored() != nil {
				return nil, failure(codec.Internal, "automatic-rule-source-invalid", int64(rule.Number))
			}
			after = rule.Number
			rules = append(rules, rule)
		}
		if len(page) < autoreservation.MaxPage {
			return rules, nil
		}
	}
}

func writeVersionSuccess(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(responseDestination{ctx, destination}, resultSuccess, 2, limits, func(writer *codec.Writer) error {
		return writer.U16(Version)
	})
}

func writeFailure(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(responseDestination{ctx, destination}, resultFailure, 0, limits, func(*codec.Writer) error { return nil })
}

type responseDestination struct {
	ctx context.Context
	io.Writer
}

// Writeは応答直前の取消しと接続先への書込み失敗を安定した分類へ変換する。
func (destination responseDestination) Write(data []byte) (int, error) {
	if destination.ctx.Err() != nil {
		return 0, failure(codec.Timeout, "request-context-ended", 0)
	}
	written, err := destination.Writer.Write(data)
	if err != nil {
		return written, failure(codec.PeerDisconnect, "response-write-failed", int64(written))
	}
	return written, nil
}

func failure(category codec.Category, reason string, size int64) error {
	return &codec.Error{Category: category, Reason: reason, Size: size}
}
