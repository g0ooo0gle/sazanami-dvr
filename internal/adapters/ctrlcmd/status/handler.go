// Package statusは実装済みのGET_STATUS_NOTIFY2最小範囲をCtrlCmdのbyte列へ変換する。
package status

import (
	"context"
	"io"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
)

const (
	Command              int32  = 2200
	Version              uint16 = 5
	TargetZero           uint32 = 0
	ResultSuccess        int32  = 1
	ResultUnsupported    int32  = 203
	StatusNotificationID uint32 = 100
	RequestBodySize             = 6
	StructureExtent             = 54
	ResponseBodySize            = 56
	ResponseFrameSize           = 64
)

// StartupStatusは起動状態を表す。現在の採用範囲はNormalだけである。
type StartupStatus uint32

const (
	StartupNormal       StartupStatus = 0
	StartupRecording    StartupStatus = 1
	StartupEPGGathering StartupStatus = 2
)

// Sourceはtunerやstreamを開かずに起動状態を返す。
type Source interface {
	Current(context.Context) (StartupStatus, error)
}

// Clockはprotocol timestampをtestで再現可能にするため外部から注入する。
type Clock interface {
	Now() time.Time
}

// Handlerはcommand 2200、Cmd2 version 5、target_count zeroだけを実装する。
type Handler struct {
	Source Source
	Clock  Clock
	Limits codec.Limits
}

var japanStandardTime = time.FixedZone("Asia/Tokyo", 9*60*60)

// Handleは14 byteのrequest全体を検証してから状態を参照し、64 byteのresponseを生成する。
// 検証失敗時に部分responseやProvider accessを発生させないことが互換境界の重要な条件である。
func (h Handler) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	frame, err := codec.ParseRequestFrame(request, h.Limits)
	if err != nil {
		return err
	}
	if frame.Code != Command {
		return &codec.Error{Category: codec.Unsupported, Reason: "command-out-of-profile"}
	}
	if len(frame.Body) != RequestBodySize {
		return &codec.Error{Category: codec.Malformed, Reason: "request-body-size", Size: int64(len(frame.Body))}
	}
	reader, err := codec.NewReader(frame.Body, h.Limits)
	if err != nil {
		return err
	}
	version, err := reader.U16()
	if err != nil {
		return err
	}
	target, err := reader.U32()
	if err != nil {
		return err
	}
	if err := reader.Exact(); err != nil {
		return err
	}
	if version != Version || target != TargetZero {
		return unsupported(destination, h.Limits)
	}
	if h.Source == nil || h.Clock == nil {
		return &codec.Error{Category: codec.Internal, Reason: "missing-status-dependency"}
	}
	startup, err := h.Source.Current(ctx)
	if err != nil {
		return err
	}
	if startup != StartupNormal {
		return &codec.Error{Category: codec.Unsupported, Reason: "startup-status-out-of-profile"}
	}
	instant := h.Clock.Now()
	if instant.IsZero() {
		return &codec.Error{Category: codec.Internal, Reason: "zero-clock-instant"}
	}
	local := instant.UTC().In(japanStandardTime)
	if local.Year() < 1 || local.Year() > 65_535 {
		return &codec.Error{Category: codec.Internal, Reason: "clock-year-out-of-wire-range"}
	}

	return codec.WriteFrame(destination, ResultSuccess, ResponseBodySize, h.Limits, func(writer *codec.Writer) error {
		if err := writer.U16(Version); err != nil {
			return err
		}
		if err := writer.I32(StructureExtent); err != nil {
			return err
		}
		if err := writer.U32(StatusNotificationID); err != nil {
			return err
		}
		fields := [...]uint16{
			uint16(local.Year()), uint16(local.Month()), uint16(local.Weekday()), uint16(local.Day()),
			uint16(local.Hour()), uint16(local.Minute()), uint16(local.Second()), uint16(local.Nanosecond() / int(time.Millisecond)),
		}
		for _, value := range fields {
			if err := writer.U16(value); err != nil {
				return err
			}
		}
		for _, value := range [...]uint32{uint32(startup), 0, 0} {
			if err := writer.U32(value); err != nil {
				return err
			}
		}
		for range 3 {
			if err := writer.String(""); err != nil {
				return err
			}
		}
		return nil
	})
}

func unsupported(destination io.Writer, limits codec.Limits) error {
	return codec.WriteFrame(destination, ResultUnsupported, 0, limits, func(*codec.Writer) error { return nil })
}
