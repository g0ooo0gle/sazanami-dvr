package runtime

import (
	"context"
	"io"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programguide"
	reservationadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/reservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/status"
)

// Routerは実装済みのCtrlCmdだけを固定switchで振り分ける。
// 動的登録を持たず、未対応commandは応答を書かずに安定したエラーへする。
type Router struct {
	status       status.Handler
	channel      channel.Handler
	programGuide *programguide.Handler
	reservations *reservationadapter.Handler
	limits       codec.Limits
}

// NewRecordingRouterは既存commandに番組表1029と予約2011／2013を加える。
func NewRecordingRouter(snapshot programguide.Source, operations reservationadapter.Operations, clock status.Clock, limits codec.Limits) (*Router, error) {
	if snapshot == nil || operations == nil {
		return nil, stable("recording-snapshot-failed")
	}
	router, err := NewRouter(snapshot, clock, limits)
	if err != nil {
		return nil, err
	}
	guide, err := programguide.NewHandler(snapshot, limits)
	if err != nil {
		return nil, stable("recording-snapshot-failed")
	}
	reservations := &reservationadapter.Handler{Operations: operations, Limits: limits}
	router.programGuide = guide
	router.reservations = reservations
	return router, nil
}

// NewRouterは同じ固定スナップショットを1060と1021へ接続する。
func NewRouter(snapshot channel.Source, clock status.Clock, limits codec.Limits) (*Router, error) {
	if snapshot == nil || clock == nil {
		return nil, stable("channel-snapshot-failed")
	}
	return &Router{
		status:  status.Handler{Source: normalStatus{}, Clock: clock, Limits: limits},
		channel: channel.Handler{Source: snapshot, Limits: limits},
		limits:  limits,
	}, nil
}

// Handleは外側frameを一度検証し、commandに対応する既存handlerだけを呼ぶ。
func (router *Router) Handle(ctx context.Context, request []byte, destination io.Writer) error {
	if router == nil {
		return &codec.Error{Category: codec.Internal, Reason: "missing-router"}
	}
	frame, err := codec.ParseRequestFrame(request, router.limits)
	if err != nil {
		return err
	}
	switch frame.Code {
	case status.Command:
		return router.status.Handle(ctx, request, destination)
	case channel.CommandFileCopy, channel.CommandEnumService:
		return router.channel.Handle(ctx, request, destination)
	case programguide.Command:
		if router.programGuide != nil {
			return router.programGuide.Handle(ctx, request, destination)
		}
	case reservationadapter.CommandList, reservationadapter.CommandAdd:
		if router.reservations != nil {
			return router.reservations.Handle(ctx, request, destination)
		}
	default:
	}
	return &codec.Error{Category: codec.Unsupported, Reason: "command-out-of-profile"}
}

type normalStatus struct{}

// Currentは外部状態を参照せず、通常起動時の固定状態だけを返す。
func (normalStatus) Current(ctx context.Context) (status.StartupStatus, error) {
	if ctx == nil || ctx.Err() != nil {
		return 0, &codec.Error{Category: codec.Timeout, Reason: "request-context-ended"}
	}
	return status.StartupNormal, nil
}

// SystemClockはCtrlCmd境界へ現在時刻を注入する本番用clockである。
type SystemClock struct{}

// Nowはprocessの現在時刻を返す。
func (SystemClock) Now() time.Time { return time.Now() }
