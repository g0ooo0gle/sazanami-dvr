package recording

import (
	"context"
	"errors"
	"time"

	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	lateStartLimit  = 5 * time.Minute
	minimumRunTime  = 60 * time.Second
	maximumWakeSize = 1
)

// ScheduleStoreはまだ録画処理を持たない次の予約を、一件だけ返すインターフェースである。
type ScheduleStore interface {
	NextActiveReservation(context.Context) (*core.Reservation, error)
}

// ReservationExecutorは予約を録画するか、ストリームを開かず未実行として確定する。
type ReservationExecutor interface {
	Execute(context.Context, core.Reservation) (Result, error)
	Miss(context.Context, core.Reservation, core.TerminalReason) (Result, error)
}

// Timerは予約の追加通知やプロセス終了と同時に待てるタイマーを表す。
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// ScheduleClockは現在時刻と一回限りのタイマーを提供する。
type ScheduleClock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// SystemClockはOSの現在時刻と、単調時刻を含むタイマーを使う実行時時計である。
type SystemClock struct{}

// Nowは現在時刻をUTCで返す。
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// NewTimerは指定時間後に一度だけ通知するタイマーを返す。
func (SystemClock) NewTimer(duration time.Duration) Timer {
	return systemTimer{Timer: time.NewTimer(duration)}
}

type systemTimer struct{ *time.Timer }

// Cはタイマーの通知チャンネルを返す。
func (timer systemTimer) C() <-chan time.Time { return timer.Timer.C }

// SchedulerはDBを正本に、一件だけの録画枠を予約開始時刻まで待機させる。
// 定期問い合わせは行わず、予約追加通知、タイマー、プロセス終了だけで次の照合を始める。
type Scheduler struct {
	store    ScheduleStore
	executor ReservationExecutor
	clock    ScheduleClock
	wake     chan struct{}
}

// NewSchedulerは同時録画一件のschedulerを作る。
func NewScheduler(store ScheduleStore, executor ReservationExecutor, clock ScheduleClock) (*Scheduler, error) {
	if store == nil || executor == nil || clock == nil {
		return nil, errors.New("recording: invalid scheduler")
	}
	return &Scheduler{store: store, executor: executor, clock: clock, wake: make(chan struct{}, maximumWakeSize)}, nil
}

// Notifyは予約追加後に待機中のschedulerへ再照合を依頼する。
// 通知は変化の存在だけを表すため、一件を超えてqueueへ溜めない。
func (scheduler *Scheduler) Notify() {
	if scheduler == nil {
		return
	}
	select {
	case scheduler.wake <- struct{}{}:
	default:
	}
}

// Runは終了コンテキストまで予約を順に処理する。通常のストリーム失敗はDBへ確定して次の予約へ進む。
func (scheduler *Scheduler) Run(ctx context.Context) error {
	if scheduler == nil || ctx == nil {
		return errors.New("recording: invalid scheduler run")
	}
	var occupiedFrom, occupiedUntil time.Time
	for {
		reservation, err := scheduler.store.NextActiveReservation(ctx)
		if err != nil {
			return errors.New("recording: read next reservation")
		}
		if reservation == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-scheduler.wake:
				continue
			}
		}
		now := scheduler.clock.Now().UTC()
		if now.IsZero() || now.UnixMilli() < 0 {
			return errors.New("recording: invalid scheduler clock")
		}
		if reservation.Program.Start.After(now) {
			timer := scheduler.clock.NewTimer(reservation.Program.Start.Sub(now))
			if timer == nil || timer.C() == nil {
				return errors.New("recording: invalid scheduler timer")
			}
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-scheduler.wake:
				timer.Stop()
				continue
			case <-timer.C():
				continue
			}
		}
		if !occupiedUntil.IsZero() && !reservation.Program.Start.Before(occupiedFrom) &&
			reservation.Program.Start.Before(occupiedUntil) {
			if _, err := scheduler.executor.Miss(ctx, *reservation, core.ReasonRecordingSlotUnavailable); err != nil {
				return err
			}
			continue
		}
		plannedEnd := reservation.Program.Start.Add(reservation.Program.Duration)
		if now.Sub(reservation.Program.Start) > lateStartLimit || plannedEnd.Sub(now) < minimumRunTime {
			if _, err := scheduler.executor.Miss(ctx, *reservation, core.ReasonLateStartExpired); err != nil {
				return err
			}
			continue
		}
		occupiedFrom = now
		if _, err := scheduler.executor.Execute(ctx, *reservation); err != nil {
			return err
		}
		occupiedUntil = scheduler.clock.Now().UTC()
	}
}
