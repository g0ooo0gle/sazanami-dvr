// Package recordedは完成済み録画の一覧と詳細をKomorebi向けCtrlCmd形式へ変換する。
package recorded

import (
	"context"
	"io"
	"strconv"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	// CommandListはKomorebiが完成済み録画の同期に使うCtrlCmd番号である。
	CommandList int32 = 2017
	// CommandGetはKomorebiが録画一件の詳細取得に使うCtrlCmd番号である。
	CommandGet int32 = 2024
	// Versionは固定版Komorebiが送るCmd2の版である。
	Version       uint16 = 5
	resultSuccess int32  = 1
	resultFailure int32  = 0
	pageSize             = 256
	responseCap          = 64 * 1024 * 1024
	fixedItemSize int64  = 73
)

// Operationsは完成録画の上限付きpage読出しと終了済み録画一件の取得を提供する。
type Operations interface {
	CompletedRecordings(context.Context, int, int32) ([]recording.HistoryItem, error)
	RecordingHistoryItem(context.Context, int32) (*recording.HistoryItem, error)
}

// Handlerは録画履歴の読出しだけをCtrlCmdへ公開する。
type Handler struct {
	Operations Operations
	Limits     codec.Limits
}

// Handleは2017と2024だけを受け付け、不正要求と未完成録画を失敗応答へ変換する。
func (handler Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	frame, err := codec.ParseRequestFrame(request, handler.Limits)
	if err != nil {
		return err
	}
	if ctx == nil || destination == nil || handler.Operations == nil {
		return fail(codec.Internal, "missing-recorded-dependency", 0)
	}
	switch frame.Code {
	case CommandList:
		return handler.list(ctx, frame.Body, destination)
	case CommandGet:
		return handler.get(ctx, frame.Body, destination)
	default:
		return fail(codec.Unsupported, "command-out-of-profile", int64(frame.Code))
	}
}

func (handler Handler) list(ctx context.Context, body []byte, destination io.Writer) error {
	if !decodeVersion(body, handler.Limits) {
		return writeFailure(ctx, destination, handler.Limits)
	}
	limits := handler.Limits
	if limits.ResponseBody == 0 || limits.ResponseBody > responseCap {
		limits.ResponseBody = responseCap
	}
	count, itemBytes, err := measure(ctx, handler.Operations, limits)
	if err != nil {
		return err
	}
	bodySize := int64(10) + itemBytes
	return codec.WriteFrame(responseDestination{ctx, destination}, resultSuccess, bodySize, limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		if err := writer.I32(int32(8 + itemBytes)); err != nil {
			return err
		}
		if err := writer.I32(int32(count)); err != nil {
			return err
		}
		return writeItems(ctx, writer, handler.Operations, limits, count)
	})
}

func (handler Handler) get(ctx context.Context, body []byte, destination io.Writer) error {
	reader, err := codec.NewReader(body, handler.Limits)
	if err != nil {
		return err
	}
	version, versionErr := reader.U16()
	number, numberErr := reader.I32()
	if versionErr != nil || numberErr != nil || version != Version || number < 1 || reader.Exact() != nil {
		return writeFailure(ctx, destination, handler.Limits)
	}
	item, err := handler.Operations.RecordingHistoryItem(ctx, number)
	if err != nil {
		return fail(codec.Internal, "recorded-source-failed", int64(number))
	}
	if item == nil || !item.Playable() {
		return writeFailure(ctx, destination, handler.Limits)
	}
	size, err := itemSize(*item, handler.Limits)
	if err != nil {
		return err
	}
	return codec.WriteFrame(responseDestination{ctx, destination}, resultSuccess, 2+size, handler.Limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		return writeItem(writer, *item)
	})
}

func decodeVersion(body []byte, limits codec.Limits) bool {
	if len(body) != 2 {
		return false
	}
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return false
	}
	version, err := reader.U16()
	return err == nil && version == Version && reader.Exact() == nil
}

func measure(ctx context.Context, operations Operations, limits codec.Limits) (int, int64, error) {
	count := 0
	var size int64
	err := forEach(ctx, operations, recording.MaxHistoryItems, func(item recording.HistoryItem) error {
		count++
		itemBytes, err := itemSize(item, limits)
		if err != nil {
			return err
		}
		if size > int64(limits.ResponseBody)-10-itemBytes {
			return fail(codec.OverLimit, "recorded-response-body", size)
		}
		size += itemBytes
		return nil
	})
	return count, size, err
}

func itemSize(item recording.HistoryItem, limits codec.Limits) (int64, error) {
	if !item.Playable() {
		return 0, fail(codec.Internal, "invalid-stored-recording", int64(item.Number))
	}
	strings := [...]string{virtualPath(item.Number), item.Title, item.StationName, "", "", ""}
	size := fixedItemSize
	for _, value := range strings {
		part, err := codec.StringSize(value, limits)
		if err != nil {
			return 0, err
		}
		size += part
	}
	if size > 1<<31-1 {
		return 0, fail(codec.OverLimit, "recorded-item-size", size)
	}
	return size, nil
}

func writeItems(ctx context.Context, writer *codec.Writer, operations Operations, limits codec.Limits, expected int) error {
	after := int32(0)
	written := 0
	for written < expected {
		if err := ctx.Err(); err != nil {
			return fail(codec.Timeout, "request-context-ended", 0)
		}
		limit := pageSize
		if expected-written < limit {
			limit = expected - written
		}
		page, err := operations.CompletedRecordings(ctx, limit, after)
		if err != nil || len(page) == 0 || len(page) > limit {
			return fail(codec.Internal, "recorded-source-changed", int64(written))
		}
		for _, item := range page {
			if item.Number <= after || !item.Playable() {
				return fail(codec.Internal, "recorded-source-order", int64(item.Number))
			}
			after = item.Number
			if err := writeItem(writer, item); err != nil {
				return err
			}
			written++
		}
	}
	return nil
}

func writeItem(writer *codec.Writer, item recording.HistoryItem) error {
	size, err := itemSize(item, codec.Limits{})
	if err != nil {
		return err
	}
	duration := item.ActualEnd.Sub(*item.ActualStart) / time.Second
	if duration < 1 || duration > 24*60*60 {
		return fail(codec.Internal, "invalid-recorded-duration", int64(duration))
	}
	if err := writer.I32(int32(size)); err != nil {
		return err
	}
	if err := writer.I32(item.Number); err != nil {
		return err
	}
	if err := writer.String(virtualPath(item.Number)); err != nil {
		return err
	}
	if err := writer.String(item.Title); err != nil {
		return err
	}
	if err := writer.SystemTime(*item.ActualStart); err != nil {
		return err
	}
	if err := writer.I32(int32(duration)); err != nil {
		return err
	}
	if err := writer.String(item.StationName); err != nil {
		return err
	}
	for _, value := range [...]uint16{item.NetworkID, item.TransportStreamID, item.ServiceID, item.EventID} {
		if err := writer.U16(value); err != nil {
			return err
		}
	}
	for range 2 {
		if err := writer.I64(0); err != nil {
			return err
		}
	}
	if err := writer.I32(1); err != nil {
		return err
	}
	if err := writer.SystemTime(item.PlannedStart); err != nil {
		return err
	}
	for range 3 {
		if err := writer.String(""); err != nil {
			return err
		}
	}
	return writer.U8(0)
}

func forEach(ctx context.Context, operations Operations, maximum int, visit func(recording.HistoryItem) error) error {
	after := int32(0)
	count := 0
	for count < maximum {
		if err := ctx.Err(); err != nil {
			return fail(codec.Timeout, "request-context-ended", 0)
		}
		page, err := operations.CompletedRecordings(ctx, pageSize, after)
		if err != nil || len(page) > pageSize {
			return fail(codec.Internal, "recorded-source-failed", int64(len(page)))
		}
		for _, item := range page {
			if item.Number <= after || !item.Playable() {
				return fail(codec.Internal, "recorded-source-order", int64(item.Number))
			}
			after = item.Number
			count++
			if count > maximum {
				return fail(codec.OverLimit, "recorded-count", int64(count))
			}
			if err := visit(item); err != nil {
				return err
			}
		}
		if len(page) < pageSize {
			return nil
		}
	}
	page, err := operations.CompletedRecordings(ctx, 1, after)
	if err != nil {
		return fail(codec.Internal, "recorded-source-failed", 0)
	}
	if len(page) != 0 {
		return fail(codec.OverLimit, "recorded-count", int64(maximum+1))
	}
	return nil
}

func virtualPath(number int32) string {
	return "/recordings/" + strconv.FormatInt(int64(number), 10) + ".ts"
}

func writeFailure(ctx context.Context, destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(responseDestination{ctx, destination}, resultFailure, 0, limits, func(*codec.Writer) error { return nil })
}

type responseDestination struct {
	ctx         context.Context
	destination io.Writer
}

// Writeは応答を書き込む直前にrequestの取消しと接続失敗を安定したerrorへ変換する。
func (destination responseDestination) Write(data []byte) (int, error) {
	if destination.ctx == nil || destination.ctx.Err() != nil {
		return 0, fail(codec.Timeout, "request-context-ended", 0)
	}
	n, err := destination.destination.Write(data)
	if err != nil {
		return n, fail(codec.PeerDisconnect, "response-write-failed", int64(n))
	}
	return n, nil
}

func fail(category codec.Category, reason string, size int64) error {
	return &codec.Error{Category: category, Reason: reason, Size: size}
}
