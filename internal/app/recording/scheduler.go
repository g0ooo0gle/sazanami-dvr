package recording

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	lateStartLimit                     = 5 * time.Minute
	minimumRunTime                     = 60 * time.Second
	startupLeadTime                    = 5 * time.Second
	maximumWakeSize                    = 1
	DefaultMaximumConcurrentRecordings = 1
	MaximumConcurrentRecordings        = 8
)

// ScheduleStoreはまだ録画処理を持たない次の予約を、一件だけ返すインターフェースである。
type ScheduleStore interface {
	NextActiveReservation(context.Context) (*core.Reservation, error)
}

// ReservationExecutorは予約をDBへ確保し、確保済み録画を実行するか未実行として確定する。
type ReservationExecutor interface {
	Claim(context.Context, core.Reservation) (core.Attempt, error)
	ExecuteClaimed(context.Context, core.Reservation, core.Attempt) (Result, error)
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

// SchedulerはDBを正本に、明示上限までの録画を予約開始時刻に起動する。
// DB確保後だけ録画用Go routineを作り、予約追加通知、タイマー、録画完了、プロセス終了で次を照合する。
type Scheduler struct {
	store             ScheduleStore
	executor          ReservationExecutor
	clock             ScheduleClock
	maximumConcurrent int
	wake              chan struct{}
	stops             *stopRegistry
}

type executionCompletion struct{ err error }

// NewSchedulerは既定一件、最大八件の範囲で録画を実行するschedulerを作る。
func NewScheduler(store ScheduleStore, executor ReservationExecutor, clock ScheduleClock, maximumConcurrent int) (*Scheduler, error) {
	if store == nil || executor == nil || clock == nil || maximumConcurrent < DefaultMaximumConcurrentRecordings ||
		maximumConcurrent > MaximumConcurrentRecordings {
		return nil, errors.New("recording: invalid scheduler")
	}
	return &Scheduler{
		store: store, executor: executor, clock: clock, maximumConcurrent: maximumConcurrent,
		wake: make(chan struct{}, maximumWakeSize), stops: newStopRegistry(maximumConcurrent),
	}, nil
}

// NotifyStopはDBへ確定済みの利用者停止を、対象の実行中録画だけへ通知する。
// 未登録でもDB確認で停止するため、通知の取りこぼしは失敗にしない。
func (scheduler *Scheduler) NotifyStop(id catalogmodel.ID) {
	if scheduler != nil {
		scheduler.stops.notify(id)
	}
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

// Runは終了コンテキストまで予約を処理する。DBへ確保済みの録画だけを上限内で並行実行する。
func (scheduler *Scheduler) Run(ctx context.Context) error {
	if scheduler == nil || ctx == nil || scheduler.maximumConcurrent < DefaultMaximumConcurrentRecordings ||
		scheduler.maximumConcurrent > MaximumConcurrentRecordings {
		return errors.New("recording: invalid scheduler run")
	}
	executionContext, cancelExecutions := context.WithCancel(ctx)
	defer cancelExecutions()
	completed := make(chan executionCompletion, scheduler.maximumConcurrent)
	active := 0
	for {
		select {
		case completion := <-completed:
			active--
			if completion.err != nil {
				return scheduler.stopExecutions(cancelExecutions, completed, active, completion.err)
			}
			continue
		default:
		}
		reservation, err := scheduler.store.NextActiveReservation(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return scheduler.stopExecutions(cancelExecutions, completed, active, nil)
			}
			return scheduler.stopExecutions(cancelExecutions, completed, active, errors.New("recording: read next reservation"))
		}
		if reservation == nil {
			completion, stopped := scheduler.wait(ctx, nil, completed)
			if stopped {
				return scheduler.stopExecutions(cancelExecutions, completed, active, nil)
			}
			if completion != nil {
				active--
				if completion.err != nil {
					return scheduler.stopExecutions(cancelExecutions, completed, active, completion.err)
				}
			}
			continue
		}
		now := scheduler.clock.Now().UTC()
		if now.IsZero() || now.UnixMilli() < 0 {
			return scheduler.stopExecutions(cancelExecutions, completed, active, errors.New("recording: invalid scheduler clock"))
		}
		wakeAt := reservation.Program.Start.Add(-startupLeadTime)
		if wakeAt.After(now) {
			timer := scheduler.clock.NewTimer(wakeAt.Sub(now))
			if timer == nil || timer.C() == nil {
				return scheduler.stopExecutions(cancelExecutions, completed, active, errors.New("recording: invalid scheduler timer"))
			}
			completion, stopped := scheduler.wait(ctx, timer, completed)
			if stopped {
				timer.Stop()
				return scheduler.stopExecutions(cancelExecutions, completed, active, nil)
			}
			timer.Stop()
			if completion != nil {
				active--
				if completion.err != nil {
					return scheduler.stopExecutions(cancelExecutions, completed, active, completion.err)
				}
			}
			continue
		}
		plannedEnd := reservation.Program.Start.Add(reservation.Program.Duration)
		if now.Sub(reservation.Program.Start) > lateStartLimit || plannedEnd.Sub(now) < minimumRunTime {
			if _, err := scheduler.executor.Miss(ctx, *reservation, core.ReasonLateStartExpired); err != nil {
				return scheduler.stopExecutions(cancelExecutions, completed, active, err)
			}
			continue
		}
		if active >= scheduler.maximumConcurrent && now.Before(reservation.Program.Start) {
			timer := scheduler.clock.NewTimer(reservation.Program.Start.Sub(now))
			if timer == nil || timer.C() == nil {
				return scheduler.stopExecutions(cancelExecutions, completed, active, errors.New("recording: invalid scheduler timer"))
			}
			completion, stopped := scheduler.wait(ctx, timer, completed)
			timer.Stop()
			if stopped {
				return scheduler.stopExecutions(cancelExecutions, completed, active, nil)
			}
			if completion == nil {
				continue
			}
			active--
			if completion.err != nil {
				return scheduler.stopExecutions(cancelExecutions, completed, active, completion.err)
			}
		}
		if active >= scheduler.maximumConcurrent {
			select {
			case completion := <-completed:
				active--
				if completion.err != nil {
					return scheduler.stopExecutions(cancelExecutions, completed, active, completion.err)
				}
			default:
				if _, err := scheduler.executor.Miss(ctx, *reservation, core.ReasonRecordingSlotUnavailable); err != nil {
					return scheduler.stopExecutions(cancelExecutions, completed, active, err)
				}
				continue
			}
		}
		attempt, err := scheduler.executor.Claim(executionContext, *reservation)
		if err != nil {
			return scheduler.stopExecutions(cancelExecutions, completed, active, err)
		}
		recordingContext, cancelRecording := context.WithCancel(executionContext)
		unregister, err := scheduler.stops.register(reservation.ID, cancelRecording)
		if err != nil {
			cancelRecording()
			return scheduler.stopExecutions(cancelExecutions, completed, active, err)
		}
		active++
		go func(item core.Reservation, claimed core.Attempt) {
			defer unregister()
			defer cancelRecording()
			_, executeErr := scheduler.executor.ExecuteClaimed(recordingContext, item, claimed)
			completed <- executionCompletion{err: executeErr}
		}(*reservation, attempt)
	}
}

func (scheduler *Scheduler) wait(ctx context.Context, timer Timer, completed <-chan executionCompletion) (*executionCompletion, bool) {
	var timerChannel <-chan time.Time
	if timer != nil {
		timerChannel = timer.C()
	}
	select {
	case <-ctx.Done():
		return nil, true
	case <-scheduler.wake:
		return nil, false
	case <-timerChannel:
		return nil, false
	case completion := <-completed:
		return &completion, false
	}
}

func (scheduler *Scheduler) stopExecutions(cancel context.CancelFunc, completed <-chan executionCompletion, active int, first error) error {
	cancel()
	for active > 0 {
		completion := <-completed
		active--
		if first == nil && completion.err != nil {
			first = completion.err
		}
	}
	return first
}
