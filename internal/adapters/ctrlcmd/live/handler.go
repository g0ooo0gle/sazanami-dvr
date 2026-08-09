// Package liveはCtrlCmd 1073、301、1074を一時的なライブ利用へ変換する。
package live

import (
	"context"
	"io"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/liverelay"
)

const (
	// CommandRelayは選択済みライブ利用からMPEG-TSを受け取るCtrlCmd番号である。
	CommandRelay int32 = 301
	// CommandSelectは放送サービスとNetworkTV IDを選択するCtrlCmd番号である。
	CommandSelect int32 = 1073
	// CommandCloseはNetworkTV IDのライブ利用を終了するCtrlCmd番号である。
	CommandClose  int32 = 1074
	resultSuccess int32 = 1
)

// OperationsはCtrlCmdのbyte形式を知らないライブ利用境界である。
type Operations interface {
	Select(context.Context, liverelay.Service, int32) (int32, error)
	Open(context.Context, int32) (liverelay.Stream, error)
	Close(int32)
}

// Handlerは三つのライブcommandだけを厳格に読み、Provider形式を外へ漏らさない。
type Handler struct {
	Operations Operations
	Limits     codec.Limits
}

// Handleは要求を検証し、301では成功header後に同じ出力先へTSを逐次転送する。
func (handler Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	if handler.Operations == nil || ctx == nil || destination == nil {
		return failure(codec.Internal, "live-dependency-missing")
	}
	frame, err := codec.ParseRequestFrame(request, handler.Limits)
	if err != nil {
		return err
	}
	switch frame.Code {
	case CommandSelect:
		return handler.selectService(ctx, frame.Body, destination)
	case CommandRelay:
		return handler.relay(ctx, frame.Body, destination)
	case CommandClose:
		return handler.close(ctx, frame.Body, destination)
	default:
		return failure(codec.Unsupported, "command-out-of-profile")
	}
}

func (handler Handler) selectService(ctx context.Context, body []byte, destination io.Writer) error {
	reader, err := codec.NewReader(body, handler.Limits)
	if err != nil {
		return err
	}
	var service liverelay.Service
	var networkTVID int32
	err = reader.Structure(func(structure *codec.Reader) error {
		useService, readErr := structure.I32()
		if readErr != nil {
			return readErr
		}
		service.NetworkID, readErr = structure.U16()
		if readErr != nil {
			return readErr
		}
		service.TransportStreamID, readErr = structure.U16()
		if readErr != nil {
			return readErr
		}
		service.ServiceID, readErr = structure.U16()
		if readErr != nil {
			return readErr
		}
		useNetworkTV, readErr := structure.I32()
		if readErr != nil {
			return readErr
		}
		networkTVID, readErr = structure.I32()
		if readErr != nil {
			return readErr
		}
		mode, readErr := structure.I32()
		if readErr != nil {
			return readErr
		}
		if useService != 1 || useNetworkTV != 1 || networkTVID <= 0 || mode != 2 ||
			service.NetworkID == 0 || service.TransportStreamID == 0 || service.ServiceID == 0 {
			return failure(codec.Malformed, "live-selection-fields")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := reader.Exact(); err != nil {
		return err
	}
	processID, err := handler.Operations.Select(ctx, service, networkTVID)
	if err != nil || processID <= 0 {
		return failure(codec.Saturated, "live-selection-failed")
	}
	return codec.WriteFrame(destination, resultSuccess, 4, handler.Limits, func(writer *codec.Writer) error {
		return writer.I32(processID)
	})
}

func (handler Handler) relay(ctx context.Context, body []byte, destination io.Writer) error {
	processID, err := singlePositiveInt(body, handler.Limits, "live-process-id")
	if err != nil {
		return err
	}
	relay, err := handler.Operations.Open(ctx, processID)
	if err != nil {
		return failure(codec.Saturated, "live-relay-open-failed")
	}
	defer relay.Close()
	if err := codec.WriteFrame(destination, resultSuccess, 0, handler.Limits, func(*codec.Writer) error { return nil }); err != nil {
		return err
	}
	return relay.Copy(destination)
}

func (handler Handler) close(ctx context.Context, body []byte, destination io.Writer) error {
	networkTVID, err := singlePositiveInt(body, handler.Limits, "live-network-tv-id")
	if err != nil {
		return err
	}
	handler.Operations.Close(networkTVID)
	if err := ctx.Err(); err != nil {
		return failure(codec.Timeout, "request-context-ended")
	}
	return codec.WriteFrame(destination, resultSuccess, 0, handler.Limits, func(*codec.Writer) error { return nil })
}

func singlePositiveInt(body []byte, limits codec.Limits, reason string) (int32, error) {
	reader, err := codec.NewReader(body, limits)
	if err != nil {
		return 0, err
	}
	value, err := reader.I32()
	if err != nil {
		return 0, err
	}
	if err := reader.Exact(); err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, failure(codec.Malformed, reason)
	}
	return value, nil
}

func failure(category codec.Category, reason string) error {
	return &codec.Error{Category: category, Reason: reason}
}
