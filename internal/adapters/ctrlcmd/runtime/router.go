package runtime

import (
	"context"
	"encoding/binary"
	"io"
	"time"

	autoreservationadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/filecopy2"
	liveadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/live"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programguide"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programsearch"
	recordedadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/recorded"
	reservationadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/reservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/status"
)

// Routerは実装済みのCtrlCmdだけを固定switchで振り分ける。
// 動的登録を持たず、未対応commandは応答を書かずに安定したエラーへする。
type Router struct {
	status       status.Handler
	snapshots    SnapshotLoader
	recording    bool
	reservations *reservationadapter.Handler
	automatic    *autoreservationadapter.Handler
	recorded     *recordedadapter.Handler
	live         *liveadapter.Handler
	logos        filecopy2.LogoProvider
	searchGate   chan struct{}
	limits       codec.Limits
}

// SetPostRecordingScriptValidatorは録画用Routerの通常予約へ、専用ディレクトリの検証を接続する。
func (router *Router) SetPostRecordingScriptValidator(validator reservationadapter.ScriptValidator) error {
	if router == nil || router.reservations == nil || validator == nil {
		return stable("post-recording-script-validator-failed")
	}
	router.reservations.ScriptValidator = validator
	return nil
}

// NewRecordingRouterWithLiveは完成済み録画までのcommandへ上限付きライブ中継を追加する。
func NewRecordingRouterWithLive(snapshots SnapshotLoader, reservations reservationadapter.Operations,
	automatic autoreservationadapter.Operations, recorded recordedadapter.Operations, live liveadapter.Operations,
	logos filecopy2.LogoProvider, clock status.Clock, limits codec.Limits,
) (*Router, error) {
	if live == nil {
		return nil, stable("live-handler-failed")
	}
	if logos == nil {
		return nil, stable("logo-handler-failed")
	}
	router, err := NewRecordingRouterComplete(snapshots, reservations, automatic, recorded, clock, limits)
	if err != nil {
		return nil, err
	}
	router.live = &liveadapter.Handler{Operations: live, Limits: limits}
	router.logos = logos
	return router, nil
}

// NewRecordingRouterCompleteは通常予約、自動予約、完成録画の対応済みcommandを接続する。
func NewRecordingRouterComplete(snapshots SnapshotLoader, reservations reservationadapter.Operations,
	automatic autoreservationadapter.Operations, recorded recordedadapter.Operations, clock status.Clock,
	limits codec.Limits,
) (*Router, error) {
	if recorded == nil {
		return nil, stable("recording-history-failed")
	}
	router, err := NewRecordingRouterWithAutomatic(snapshots, reservations, automatic, clock, limits)
	if err != nil {
		return nil, err
	}
	router.recorded = &recordedadapter.Handler{Operations: recorded, Limits: limits}
	return router, nil
}

// NewRecordingRouterWithAutomaticは番組表、通常予約、自動予約の対応済みcommandを接続する。
func NewRecordingRouterWithAutomatic(snapshots SnapshotLoader, reservations reservationadapter.Operations,
	automatic autoreservationadapter.Operations, clock status.Clock, limits codec.Limits,
) (*Router, error) {
	if automatic == nil {
		return nil, stable("recording-snapshot-failed")
	}
	router, err := NewRecordingRouter(snapshots, reservations, clock, limits)
	if err != nil {
		return nil, err
	}
	router.automatic = &autoreservationadapter.Handler{Operations: automatic, Limits: limits}
	return router, nil
}

// NewRecordingRouterは番組表とKonomiTV向け予約・録画中確認commandを接続する。
func NewRecordingRouter(snapshots SnapshotLoader, operations reservationadapter.Operations, clock status.Clock, limits codec.Limits) (*Router, error) {
	if snapshots == nil || snapshots.Load() == nil || operations == nil {
		return nil, stable("recording-snapshot-failed")
	}
	router, err := NewRouter(snapshots, clock, limits)
	if err != nil {
		return nil, err
	}
	reservations := &reservationadapter.Handler{Operations: operations, Limits: limits}
	router.recording = true
	router.reservations = reservations
	return router, nil
}

// NewRouterは要求開始時に一度だけスナップショットを取得し、1060と1021へ接続する。
func NewRouter(snapshots SnapshotLoader, clock status.Clock, limits codec.Limits) (*Router, error) {
	if snapshots == nil || snapshots.Load() == nil || clock == nil {
		return nil, stable("channel-snapshot-failed")
	}
	return &Router{
		status:     status.Handler{Source: normalStatus{}, Clock: clock, Limits: limits},
		snapshots:  snapshots,
		searchGate: make(chan struct{}, 1),
		limits:     limits,
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
		snapshot := router.snapshots.Load()
		if snapshot == nil {
			return stable("channel-snapshot-failed")
		}
		return (channel.Handler{Source: snapshot, Limits: router.limits}).Handle(ctx, request, destination)
	case filecopy2.Command:
		snapshot := router.snapshots.Load()
		if snapshot == nil {
			return stable("channel-snapshot-failed")
		}
		return (filecopy2.Handler{Source: snapshot, Logos: router.logos, Limits: router.limits}).Handle(ctx, request, destination)
	case programguide.Command:
		if router.recording {
			snapshot := router.snapshots.Load()
			guide, guideErr := programguide.NewHandler(snapshot, router.limits)
			if guideErr != nil {
				return stable("recording-snapshot-failed")
			}
			return guide.Handle(ctx, request, destination)
		}
	case programsearch.Command:
		if router.recording {
			snapshot := router.snapshots.Load()
			search, searchErr := programsearch.NewHandler(snapshot, router.limits, router.searchGate)
			if searchErr != nil {
				return stable("recording-snapshot-failed")
			}
			return search.Handle(ctx, request, destination)
		}
	case reservationadapter.CommandList, reservationadapter.CommandAdd, reservationadapter.CommandChange,
		reservationadapter.CommandDelete, reservationadapter.CommandRecordingOpen, reservationadapter.CommandRecordingClose:
		if router.reservations != nil {
			return router.reservations.Handle(ctx, request, destination)
		}
	case autoreservationadapter.CommandList, autoreservationadapter.CommandAdd, autoreservationadapter.CommandChange,
		autoreservationadapter.CommandDelete:
		if router.automatic != nil {
			return router.automatic.Handle(ctx, request, destination)
		}
	case recordedadapter.CommandList, recordedadapter.CommandGet:
		if router.recorded != nil {
			return router.recorded.Handle(ctx, request, destination)
		}
	case liveadapter.CommandSelect, liveadapter.CommandRelay, liveadapter.CommandClose:
		if router.live != nil {
			return router.live.Handle(ctx, request, destination)
		}
	default:
	}
	return &codec.Error{Category: codec.Unsupported, Reason: "command-out-of-profile"}
}

// LongLivedはライブ中継が接続済みの301要求だけを長時間接続として識別する。
func (router *Router) LongLived(request []byte) bool {
	return router != nil && router.live != nil && len(request) >= codec.HeaderSize &&
		int32(binary.LittleEndian.Uint32(request[:4])) == liveadapter.CommandRelay
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
