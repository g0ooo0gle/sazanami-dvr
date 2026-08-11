package recording

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type noOpPostPowerDecision struct{}

func (noOpPostPowerDecision) LockPostRecordingPowerDecision()   {}
func (noOpPostPowerDecision) UnlockPostRecordingPowerDecision() {}

type queueStore struct {
	noOpPostPowerDecision
	mu      sync.Mutex
	items   []core.Reservation
	queries int
	queried chan struct{}
}

func (store *queueStore) ExpireOneDisabledReservation(context.Context, time.Time) (bool, error) {
	return false, nil
}

func (store *queueStore) NextDisabledReservationDeadline(context.Context, time.Time) (*time.Time, error) {
	return nil, nil
}

func (store *queueStore) NextActiveReservation(context.Context, time.Time) (*core.Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.queries++
	if store.queried != nil {
		select {
		case store.queried <- struct{}{}:
		default:
		}
	}
	if len(store.items) == 0 {
		return nil, nil
	}
	item := store.items[0]
	store.items = store.items[1:]
	return &item, nil
}

type schedulerExecutor struct {
	clock    *manualScheduleClock
	claimed  []core.Reservation
	execute  []core.Reservation
	miss     []core.TerminalReason
	executed chan struct{}
	release  <-chan struct{}
	claim    func(core.Reservation) (core.Attempt, error)
}

func (executor *schedulerExecutor) Claim(_ context.Context, reservation core.Reservation) (core.Attempt, error) {
	executor.claimed = append(executor.claimed, reservation)
	if executor.claim != nil {
		return executor.claim(reservation)
	}
	return core.Attempt{ReservationID: reservation.ID}, nil
}

func (executor *schedulerExecutor) ExecuteClaimed(ctx context.Context, reservation core.Reservation, _ core.Attempt) (Result, error) {
	executor.execute = append(executor.execute, reservation)
	if executor.executed != nil {
		select {
		case executor.executed <- struct{}{}:
		default:
		}
	}
	if executor.release != nil {
		select {
		case <-ctx.Done():
			return Result{}, nil
		case <-executor.release:
		}
	}
	return Result{State: core.AttemptSucceeded, Reason: core.ReasonCompleted}, nil
}

func (executor *schedulerExecutor) Miss(_ context.Context, _ core.Reservation, reason core.TerminalReason) (Result, error) {
	executor.miss = append(executor.miss, reason)
	return Result{State: core.AttemptMissed, Reason: reason}, nil
}

type manualScheduleClock struct {
	now      time.Time
	timer    *manualTimer
	duration time.Duration
	created  chan struct{}
}

func (clock *manualScheduleClock) Now() time.Time { return clock.now }
func (clock *manualScheduleClock) NewTimer(duration time.Duration) Timer {
	clock.duration = duration
	clock.timer = &manualTimer{channel: make(chan time.Time, 1)}
	if clock.created != nil {
		select {
		case clock.created <- struct{}{}:
		default:
		}
	}
	return clock.timer
}

type manualTimer struct{ channel chan time.Time }

func (timer *manualTimer) C() <-chan time.Time { return timer.channel }
func (timer *manualTimer) Stop() bool          { return true }

type disabledDeadlineStore struct {
	noOpPostPowerDecision
	deadline time.Time
	expired  bool
	calls    chan struct{}
}

func (store *disabledDeadlineStore) ExpireOneDisabledReservation(_ context.Context, now time.Time) (bool, error) {
	if !store.expired && !now.Before(store.deadline) {
		store.expired = true
		return true, nil
	}
	return false, nil
}

func (store *disabledDeadlineStore) NextDisabledReservationDeadline(context.Context, time.Time) (*time.Time, error) {
	if store.expired {
		select {
		case store.calls <- struct{}{}:
		default:
		}
		return nil, nil
	}
	deadline := store.deadline
	return &deadline, nil
}

func (*disabledDeadlineStore) NextActiveReservation(context.Context, time.Time) (*core.Reservation, error) {
	return nil, nil
}

func TestSchedulerWakesForDisabledReservationDeadline(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	clock := &manualScheduleClock{now: now, created: make(chan struct{}, 1)}
	store := &disabledDeadlineStore{deadline: now.Add(time.Minute), calls: make(chan struct{}, 1)}
	scheduler, err := NewScheduler(store, &schedulerExecutor{clock: clock}, clock, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-clock.created:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("無効予約の終了timerが作られませんでした")
	}
	if clock.duration != time.Minute {
		cancel()
		t.Fatalf("duration=%s", clock.duration)
	}
	clock.now = store.deadline
	clock.timer.channel <- store.deadline
	select {
	case <-store.calls:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("期限後に無効予約が終了しませんでした")
	}
	cancel()
	if err := <-done; err != nil || !store.expired {
		t.Fatalf("expired=%v err=%v", store.expired, err)
	}
}

func TestSchedulerUsesOneSlotAndLateStartRules(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &manualScheduleClock{now: start}
	first := reservationForExecutor(t, start, 10*time.Minute)
	second := reservationForExecutor(t, start, 10*time.Minute)
	second.ID = appID(t, 40)
	late := reservationForExecutor(t, start.Add(-10*time.Minute), 20*time.Minute)
	late.ID = appID(t, 41)
	store := &queueStore{items: []core.Reservation{first, second, late}, queried: make(chan struct{}, 8)}
	release := make(chan struct{})
	executor := &schedulerExecutor{clock: clock, release: release}
	scheduler, err := NewScheduler(store, executor, clock, DefaultMaximumConcurrentRecordings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	for index := 0; index < 4; index++ {
		select {
		case <-store.queried:
		case <-time.After(time.Second):
			t.Fatal("schedulerが次の予約を確認しませんでした")
		}
	}
	close(release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(executor.execute) != 1 || len(executor.miss) != 2 ||
		executor.miss[0] != core.ReasonRecordingSlotUnavailable || executor.miss[1] != core.ReasonLateStartExpired {
		t.Fatalf("execute=%d miss=%v", len(executor.execute), executor.miss)
	}
}

func TestSchedulerReloadsReservationAfterClaimConflict(t *testing.T) {
	start := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	old := reservationForExecutor(t, start, 10*time.Minute)
	old.Version = 1
	current := old
	current.Version = 2
	store := &queueStore{items: []core.Reservation{old}}
	claims := 0
	executor := &schedulerExecutor{clock: &manualScheduleClock{now: start}, executed: make(chan struct{}, 1)}
	executor.claim = func(reservation core.Reservation) (core.Attempt, error) {
		claims++
		if claims == 1 {
			store.mu.Lock()
			store.items = append(store.items, current)
			store.mu.Unlock()
			return core.Attempt{}, core.ErrReservationUnavailable
		}
		return core.Attempt{ReservationID: reservation.ID}, nil
	}
	scheduler, err := NewScheduler(store, executor, executor.clock, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-executor.executed:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("変更後の予約が実行されませんでした")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(executor.claimed) != 2 || executor.claimed[0].Version != old.Version ||
		executor.claimed[1].Version != current.Version || len(executor.execute) != 1 ||
		executor.execute[0].Version != current.Version {
		t.Fatalf("claimed=%v execute=%v", executor.claimed, executor.execute)
	}
}

func TestSchedulerReloadsLateReservationAfterMissConflict(t *testing.T) {
	now := time.Date(2026, 8, 12, 2, 0, 0, 0, time.UTC)
	old := reservationForExecutor(t, now.Add(-10*time.Minute), 20*time.Minute)
	old.Version = 1
	current := old
	current.Version = 2
	store := &queueStore{items: []core.Reservation{old}, queried: make(chan struct{}, 4)}
	misses := 0
	versions := make([]int64, 0, 2)
	executor := &concurrentSchedulerExecutor{}
	executor.onMiss = func(reservation core.Reservation, reason core.TerminalReason) error {
		misses++
		versions = append(versions, reservation.Version)
		if reason != core.ReasonLateStartExpired {
			return errors.New("unexpected late miss reason")
		}
		if misses == 1 {
			store.mu.Lock()
			store.items = append(store.items, current)
			store.mu.Unlock()
			return core.ErrReservationUnavailable
		}
		return nil
	}
	scheduler, err := NewScheduler(store, executor, &manualScheduleClock{now: now}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	for range 3 {
		select {
		case <-store.queried:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("変更後の遅延予約が再確認されませんでした")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if misses != 2 || len(versions) != 2 || versions[0] != old.Version || versions[1] != current.Version ||
		len(executor.missed) != 1 || executor.missed[0] != core.ReasonLateStartExpired {
		t.Fatalf("misses=%d versions=%v results=%v", misses, versions, executor.missed)
	}
}

func TestSchedulerUsesPlannedEndForMinimumRemainingTime(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	reservation := reservationForExecutor(t, start, 4*time.Minute)
	for _, test := range []struct {
		name      string
		remaining time.Duration
		wantRun   bool
	}{
		{name: "sixty seconds", remaining: 60 * time.Second, wantRun: true},
		{name: "one second under", remaining: 59 * time.Second, wantRun: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &manualScheduleClock{now: reservation.PlannedEnd().Add(-test.remaining)}
			store := &queueStore{items: []core.Reservation{reservation}, queried: make(chan struct{}, 4)}
			executor := &schedulerExecutor{clock: clock, executed: make(chan struct{}, 1)}
			scheduler, err := NewScheduler(store, executor, clock, 1)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- scheduler.Run(ctx) }()
			if test.wantRun {
				select {
				case <-executor.executed:
				case <-time.After(time.Second):
					cancel()
					t.Fatal("残り60秒の予約が始まりませんでした")
				}
			} else {
				for range 2 {
					select {
					case <-store.queried:
					case <-time.After(time.Second):
						cancel()
						t.Fatal("残り時間不足が判定されませんでした")
					}
				}
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if test.wantRun && len(executor.execute) != 1 || !test.wantRun &&
				(len(executor.miss) != 1 || executor.miss[0] != core.ReasonLateStartExpired) {
				t.Fatalf("execute=%d miss=%v", len(executor.execute), executor.miss)
			}
		})
	}
}

func TestSchedulerWaitsForNotificationWithoutPolling(t *testing.T) {
	clock := &manualScheduleClock{now: time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)}
	store := &queueStore{queried: make(chan struct{}, 4)}
	scheduler, err := NewScheduler(store, &schedulerExecutor{clock: clock}, clock, DefaultMaximumConcurrentRecordings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-store.queried:
	case <-time.After(time.Second):
		t.Fatal("最初の予約確認が行われませんでした")
	}
	time.Sleep(20 * time.Millisecond)
	store.mu.Lock()
	queries := store.queries
	store.mu.Unlock()
	if queries != 1 {
		t.Fatalf("通知なしで%d回queryしました", queries)
	}
	scheduler.Notify()
	select {
	case <-store.queried:
	case <-time.After(time.Second):
		t.Fatal("通知後の予約確認が行われませんでした")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type schedulerErrorStore struct {
	noOpPostPowerDecision
	err    error
	before func()
}

func (store schedulerErrorStore) ExpireOneDisabledReservation(context.Context, time.Time) (bool, error) {
	return false, nil
}

func (store schedulerErrorStore) NextDisabledReservationDeadline(context.Context, time.Time) (*time.Time, error) {
	return nil, nil
}

func (store schedulerErrorStore) NextActiveReservation(context.Context, time.Time) (*core.Reservation, error) {
	if store.before != nil {
		store.before()
	}
	return nil, store.err
}

func TestSchedulerTreatsDatabaseCancellationAsCleanShutdown(t *testing.T) {
	clock := &manualScheduleClock{now: time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)}
	executor := &schedulerExecutor{clock: clock}
	ctx, cancel := context.WithCancel(context.Background())
	store := schedulerErrorStore{err: context.Canceled, before: cancel}
	scheduler, err := NewScheduler(store, executor, clock, DefaultMaximumConcurrentRecordings)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Run(ctx); err != nil {
		t.Fatalf("停止によるDB取消しが失敗扱いになりました: %v", err)
	}
}

func TestSchedulerKeepsDatabaseFailureWhileRunning(t *testing.T) {
	clock := &manualScheduleClock{now: time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)}
	executor := &schedulerExecutor{clock: clock}
	scheduler, err := NewScheduler(schedulerErrorStore{err: errors.New("database failed")}, executor, clock, DefaultMaximumConcurrentRecordings)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Run(context.Background()); err == nil {
		t.Fatal("稼働中のDB失敗が正常停止扱いになりました")
	}
}

type futureStore struct {
	noOpPostPowerDecision
	item    core.Reservation
	queries int
}

func (store *futureStore) ExpireOneDisabledReservation(context.Context, time.Time) (bool, error) {
	return false, nil
}

func (store *futureStore) NextDisabledReservationDeadline(context.Context, time.Time) (*time.Time, error) {
	return nil, nil
}

func (store *futureStore) NextActiveReservation(context.Context, time.Time) (*core.Reservation, error) {
	store.queries++
	if store.queries > 2 {
		return nil, nil
	}
	item := store.item
	return &item, nil
}

func TestSchedulerUsesTimerAndRechecksDatabase(t *testing.T) {
	start := time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)
	clock := &manualScheduleClock{now: start.Add(-time.Hour), created: make(chan struct{}, 1)}
	store := &futureStore{item: reservationForExecutor(t, start, 10*time.Minute)}
	executor := &schedulerExecutor{clock: clock, executed: make(chan struct{}, 1)}
	scheduler, err := NewScheduler(store, executor, clock, DefaultMaximumConcurrentRecordings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case <-clock.created:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("開始時刻のtimerが作られませんでした")
	}
	if clock.duration != time.Hour-core.DefaultStartMargin {
		cancel()
		t.Fatalf("timer=%s", clock.duration)
	}
	clock.now = start.Add(-core.DefaultStartMargin)
	clock.timer.channel <- clock.now
	select {
	case <-executor.executed:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("timer後に予約が実行されませんでした")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type concurrentSchedulerExecutor struct {
	mu        sync.Mutex
	claimed   int
	running   int
	maximum   int
	missed    []core.TerminalReason
	started   chan core.Reservation
	release   chan struct{}
	onClaim   func(core.Reservation)
	onMiss    func(core.Reservation, core.TerminalReason) error
	failAfter <-chan struct{}
	failID    core.Reservation
	cancelled int
}

func (executor *concurrentSchedulerExecutor) Claim(_ context.Context, reservation core.Reservation) (core.Attempt, error) {
	executor.mu.Lock()
	executor.claimed++
	executor.mu.Unlock()
	if executor.onClaim != nil {
		executor.onClaim(reservation)
	}
	return core.Attempt{ReservationID: reservation.ID}, nil
}

func (executor *concurrentSchedulerExecutor) ExecuteClaimed(ctx context.Context, reservation core.Reservation, attempt core.Attempt) (Result, error) {
	if attempt.ReservationID != reservation.ID {
		return Result{}, errors.New("attempt mismatch")
	}
	executor.mu.Lock()
	executor.running++
	if executor.running > executor.maximum {
		executor.maximum = executor.running
	}
	executor.mu.Unlock()
	executor.started <- reservation
	if executor.failAfter != nil && reservation.ID == executor.failID.ID {
		select {
		case <-ctx.Done():
			executor.finishConcurrent(true)
			return Result{}, nil
		case <-executor.failAfter:
			executor.finishConcurrent(false)
			return Result{}, errors.New("recording failed")
		}
	}
	select {
	case <-ctx.Done():
		executor.finishConcurrent(true)
		return Result{}, nil
	case <-executor.release:
		executor.finishConcurrent(false)
		return Result{State: core.AttemptSucceeded, Reason: core.ReasonCompleted,
			PostRecording: reservation.PostRecording.Mode}, nil
	}
}

func (executor *concurrentSchedulerExecutor) finishConcurrent(cancelled bool) {
	executor.mu.Lock()
	executor.running--
	if cancelled {
		executor.cancelled++
	}
	executor.mu.Unlock()
}

func (executor *concurrentSchedulerExecutor) Miss(_ context.Context, reservation core.Reservation, reason core.TerminalReason) (Result, error) {
	if executor.onMiss != nil {
		if err := executor.onMiss(reservation, reason); err != nil {
			return Result{}, err
		}
	}
	executor.mu.Lock()
	executor.missed = append(executor.missed, reason)
	executor.mu.Unlock()
	return Result{State: core.AttemptMissed, Reason: reason}, nil
}

func TestSchedulerRunsTwoRecordingsAndMissesOneOverLimit(t *testing.T) {
	start := time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	items := []core.Reservation{
		reservationForExecutor(t, start, 10*time.Minute),
		reservationForExecutor(t, start, 10*time.Minute),
		reservationForExecutor(t, start, 10*time.Minute),
	}
	items[1].ID = appID(t, 51)
	items[2].ID = appID(t, 52)
	store := &queueStore{items: items, queried: make(chan struct{}, 8)}
	executor := &concurrentSchedulerExecutor{started: make(chan core.Reservation, 3), release: make(chan struct{}, 2)}
	scheduler, err := NewScheduler(store, executor, &manualScheduleClock{now: start}, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	for range 2 {
		select {
		case <-executor.started:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("二件の録画が同時に開始されませんでした")
		}
	}
	for range 4 {
		select {
		case <-store.queried:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("三件目の枠不足が判定されませんでした")
		}
	}
	executor.release <- struct{}{}
	executor.release <- struct{}{}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.claimed != 2 || executor.maximum != 2 || executor.running != 0 ||
		len(executor.missed) != 1 || executor.missed[0] != core.ReasonRecordingSlotUnavailable {
		t.Fatalf("claimed=%d maximum=%d running=%d missed=%v", executor.claimed, executor.maximum, executor.running, executor.missed)
	}
}

func TestSchedulerReloadsReservationAfterSlotMissConflict(t *testing.T) {
	start := time.Date(2026, 8, 12, 3, 0, 0, 0, time.UTC)
	first := reservationForExecutor(t, start, 10*time.Minute)
	old := reservationForExecutor(t, start, 10*time.Minute)
	old.ID = appID(t, 81)
	old.Version = 1
	current := old
	current.Version = 2
	store := &queueStore{items: []core.Reservation{first, old}, queried: make(chan struct{}, 8)}
	misses := 0
	versions := make([]int64, 0, 2)
	executor := &concurrentSchedulerExecutor{
		started: make(chan core.Reservation, 1), release: make(chan struct{}),
	}
	executor.onMiss = func(reservation core.Reservation, reason core.TerminalReason) error {
		misses++
		versions = append(versions, reservation.Version)
		if reason != core.ReasonRecordingSlotUnavailable {
			return errors.New("unexpected slot miss reason")
		}
		if misses == 1 {
			store.mu.Lock()
			store.items = append(store.items, current)
			store.mu.Unlock()
			return core.ErrReservationUnavailable
		}
		return nil
	}
	scheduler, err := NewScheduler(store, executor, &manualScheduleClock{now: start}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	waitSchedulerStart(t, executor.started)
	for range 4 {
		select {
		case <-store.queried:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("変更後の枠不足予約が再確認されませんでした")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if misses != 2 || len(versions) != 2 || versions[0] != old.Version || versions[1] != current.Version ||
		len(executor.missed) != 1 ||
		executor.missed[0] != core.ReasonRecordingSlotUnavailable || executor.cancelled != 1 {
		t.Fatalf("misses=%d versions=%v results=%v cancelled=%d", misses, versions, executor.missed, executor.cancelled)
	}
}

func TestSchedulerReusesCompletedSlotAfterNotification(t *testing.T) {
	start := time.Date(2026, 8, 9, 9, 0, 0, 0, time.UTC)
	first := reservationForExecutor(t, start, 10*time.Minute)
	second := reservationForExecutor(t, start, 10*time.Minute)
	second.ID = appID(t, 53)
	store := &queueStore{items: []core.Reservation{first}, queried: make(chan struct{}, 8)}
	executor := &concurrentSchedulerExecutor{started: make(chan core.Reservation, 2), release: make(chan struct{}, 2)}
	scheduler, err := NewScheduler(store, executor, &manualScheduleClock{now: start}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	waitSchedulerStart(t, executor.started)
	executor.release <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for {
		executor.mu.Lock()
		running := executor.running
		executor.mu.Unlock()
		if running == 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("完了した録画枠が解放されませんでした")
		}
		time.Sleep(time.Millisecond)
	}
	store.mu.Lock()
	store.items = append(store.items, second)
	store.mu.Unlock()
	scheduler.Notify()
	started := waitSchedulerStart(t, executor.started)
	if started.ID != second.ID {
		cancel()
		t.Fatalf("別の予約が開始されました: %v", started.ID)
	}
	executor.release <- struct{}{}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type pendingScheduleStore struct {
	noOpPostPowerDecision
	mu    sync.Mutex
	items []core.Reservation
}

func (store *pendingScheduleStore) ExpireOneDisabledReservation(context.Context, time.Time) (bool, error) {
	return false, nil
}

func (store *pendingScheduleStore) NextDisabledReservationDeadline(context.Context, time.Time) (*time.Time, error) {
	return nil, nil
}

func (store *pendingScheduleStore) NextActiveReservation(context.Context, time.Time) (*core.Reservation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) == 0 {
		return nil, nil
	}
	item := store.items[0]
	return &item, nil
}

func (store *pendingScheduleStore) claimed(reservation core.Reservation) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) > 0 && store.items[0].ID == reservation.ID {
		store.items = store.items[1:]
	}
}

func TestSchedulerWaitsUntilStartForBackToBackRecordingSlot(t *testing.T) {
	firstStart := time.Date(2026, 8, 9, 9, 30, 0, 0, time.UTC)
	secondStart := firstStart.Add(10 * time.Minute)
	first := reservationForExecutor(t, firstStart, 10*time.Minute)
	second := reservationForExecutor(t, secondStart, 10*time.Minute)
	second.ID = appID(t, 56)
	store := &pendingScheduleStore{items: []core.Reservation{first, second}}
	clock := &manualScheduleClock{now: firstStart, created: make(chan struct{}, 2)}
	executor := &concurrentSchedulerExecutor{
		started: make(chan core.Reservation, 2), release: make(chan struct{}, 2), onClaim: store.claimed,
	}
	scheduler, err := NewScheduler(store, executor, clock, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	started := waitSchedulerStart(t, executor.started)
	if started.ID != first.ID {
		cancel()
		t.Fatalf("最初の予約ではありません: %v", started.ID)
	}
	select {
	case <-clock.created:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("次の予約の準備時刻timerが作られませんでした")
	}
	clock.now = secondStart.Add(-core.DefaultStartMargin)
	executor.release <- struct{}{}
	started = waitSchedulerStart(t, executor.started)
	if started.ID != second.ID {
		cancel()
		t.Fatalf("連続する予約ではありません: %v", started.ID)
	}
	executor.release <- struct{}{}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.claimed != 2 || len(executor.missed) != 0 {
		t.Fatalf("claimed=%d missed=%v", executor.claimed, executor.missed)
	}
}

func TestSchedulerCancelsAndWaitsForAllRecordings(t *testing.T) {
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	first := reservationForExecutor(t, start, 10*time.Minute)
	second := reservationForExecutor(t, start, 10*time.Minute)
	second.ID = appID(t, 54)
	store := &queueStore{items: []core.Reservation{first, second}}
	executor := &concurrentSchedulerExecutor{started: make(chan core.Reservation, 2), release: make(chan struct{})}
	scheduler, err := NewScheduler(store, executor, &manualScheduleClock{now: start}, 2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	waitSchedulerStart(t, executor.started)
	waitSchedulerStart(t, executor.started)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("停止時に録画処理の終了を待てませんでした")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.running != 0 || executor.cancelled != 2 {
		t.Fatalf("running=%d cancelled=%d", executor.running, executor.cancelled)
	}
}

func TestSchedulerCancelsSiblingAfterExecutionError(t *testing.T) {
	start := time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC)
	first := reservationForExecutor(t, start, 10*time.Minute)
	second := reservationForExecutor(t, start, 10*time.Minute)
	second.ID = appID(t, 55)
	fail := make(chan struct{})
	executor := &concurrentSchedulerExecutor{
		started: make(chan core.Reservation, 2), release: make(chan struct{}), failAfter: fail, failID: first,
	}
	scheduler, err := NewScheduler(&queueStore{items: []core.Reservation{first, second}}, executor,
		&manualScheduleClock{now: start}, 2)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(context.Background()) }()
	waitSchedulerStart(t, executor.started)
	waitSchedulerStart(t, executor.started)
	close(fail)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("録画処理の基盤エラーが失敗終了になりませんでした")
		}
	case <-time.After(time.Second):
		t.Fatal("基盤エラー後に兄弟録画を停止できませんでした")
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.running != 0 || executor.cancelled != 1 {
		t.Fatalf("running=%d cancelled=%d", executor.running, executor.cancelled)
	}
}

func TestSchedulerRejectsConcurrentBounds(t *testing.T) {
	clock := &manualScheduleClock{now: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	for _, maximum := range []int{-1, 0} {
		if _, err := NewScheduler(&queueStore{}, &schedulerExecutor{clock: clock}, clock, maximum); err == nil {
			t.Fatalf("maximum=%dが受理されました", maximum)
		}
	}
}

func TestSchedulerLargeLimitDoesNotPreallocate(t *testing.T) {
	clock := &manualScheduleClock{now: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	scheduler, err := NewScheduler(&queueStore{}, &schedulerExecutor{clock: clock}, clock, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if cap(scheduler.wake) != maximumWakeSize || len(scheduler.stops.items) != 0 {
		t.Fatalf("wake=%d stops=%d", cap(scheduler.wake), len(scheduler.stops.items))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

type postPowerRecorder struct {
	mode   core.PostRecordingMode
	wakeAt *time.Time
	calls  int
	reason string
	called chan struct{}
}

func (controller *postPowerRecorder) Execute(_ context.Context, mode core.PostRecordingMode, wakeAt *time.Time) PostRecordingPowerResult {
	controller.calls++
	controller.mode = mode
	if wakeAt != nil {
		copied := *wakeAt
		controller.wakeAt = &copied
	}
	if controller.called != nil {
		controller.called <- struct{}{}
	}
	return PostRecordingPowerResult{Reason: controller.reason}
}

func TestPostRecordingPowerCandidateCombinesTwentyOneRecordings(t *testing.T) {
	for count := 1; count <= 21; count++ {
		candidate := postRecordingPowerCandidate{}
		for range count {
			candidate.add(core.PostRecordingStandby)
		}
		if !candidate.present || candidate.conflict || candidate.mode != core.PostRecordingStandby {
			t.Fatalf("count=%d candidate=%+v", count, candidate)
		}
	}
	candidate := postRecordingPowerCandidate{}
	candidate.add(core.PostRecordingNothing)
	if candidate.present {
		t.Fatal("何もしない設定が電源候補になりました")
	}
	candidate.add(core.PostRecordingStandby)
	candidate.add(core.PostRecordingShutdown)
	if !candidate.conflict {
		t.Fatal("異なる電源候補の競合を検出できませんでした")
	}
}

func TestSchedulerEvaluatesPostRecordingPowerBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	controller := &postPowerRecorder{}
	scheduler := &Scheduler{postPower: controller}
	candidate := postRecordingPowerCandidate{mode: core.PostRecordingStandbyReboot, present: true}

	if result := scheduler.executePostRecordingPower(context.Background(), candidate, now, nil); result != (PostRecordingPowerResult{}) ||
		controller.calls != 1 || controller.mode != core.PostRecordingStandbyReboot || controller.wakeAt != nil {
		t.Fatalf("result=%+v controller=%+v", result, controller)
	}

	controller.calls = 0
	next := core.Reservation{Program: core.ProgramSnapshot{Start: now.Add(10 * time.Minute)}, Margins: &core.RecordingMargins{}}
	if result := scheduler.executePostRecordingPower(context.Background(), candidate, now, &next); result != (PostRecordingPowerResult{}) ||
		controller.calls != 1 || controller.wakeAt == nil || !controller.wakeAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("result=%+v controller=%+v", result, controller)
	}

	controller.calls = 0
	next.Program.Start = now.Add(10*time.Minute - time.Millisecond)
	if result := scheduler.executePostRecordingPower(context.Background(), candidate, now, &next); result.Reason != "post-recording-power-too-late" ||
		controller.calls != 0 {
		t.Fatalf("result=%+v calls=%d", result, controller.calls)
	}

	conflict := candidate
	conflict.conflict = true
	if result := scheduler.executePostRecordingPower(context.Background(), conflict, now, nil); result.Reason != "post-recording-power-conflict" {
		t.Fatalf("result=%+v", result)
	}
	if result := (&Scheduler{}).executePostRecordingPower(context.Background(), candidate, now, nil); result.Reason != "post-recording-power-unavailable" {
		t.Fatalf("result=%+v", result)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if result := scheduler.executePostRecordingPower(cancelled, candidate, now, nil); result.Reason != "post-recording-power-cancelled" {
		t.Fatalf("result=%+v", result)
	}
}

func TestSchedulerPostRecordingPowerSetupAndAdapterReason(t *testing.T) {
	scheduler := &Scheduler{}
	if err := scheduler.SetPostRecordingPower(nil, nil); err == nil {
		t.Fatal("nil controllerが受理されました")
	}
	controller := &postPowerRecorder{reason: "post-recording-power-failed"}
	if err := scheduler.SetPostRecordingPower(controller, nil); err != nil {
		t.Fatal(err)
	}
	result := scheduler.executePostRecordingPower(context.Background(), postRecordingPowerCandidate{
		mode: core.PostRecordingShutdown, present: true,
	}, time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC), nil)
	if result.Reason != controller.reason || controller.calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, controller.calls)
	}
}

func TestSchedulerWaitsForAllNineRecordingsBeforePowerAction(t *testing.T) {
	start := time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC)
	items := make([]core.Reservation, 9)
	for index := range items {
		items[index] = reservationForExecutor(t, start, 10*time.Minute)
		items[index].ID = appID(t, byte(100+index))
		items[index].PostRecording.Mode = core.PostRecordingStandby
	}
	executor := &concurrentSchedulerExecutor{
		started: make(chan core.Reservation, len(items)), release: make(chan struct{}, len(items)),
	}
	scheduler, err := NewScheduler(&queueStore{items: items}, executor, &manualScheduleClock{now: start}, len(items))
	if err != nil {
		t.Fatal(err)
	}
	controller := &postPowerRecorder{called: make(chan struct{}, 1)}
	if err := scheduler.SetPostRecordingPower(controller, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	for range items {
		waitSchedulerStart(t, executor.started)
	}
	for range len(items) - 1 {
		executor.release <- struct{}{}
	}
	deadline := time.Now().Add(time.Second)
	for {
		executor.mu.Lock()
		running := executor.running
		executor.mu.Unlock()
		if running == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("先行する録画の完了を確認できませんでした")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-controller.called:
		cancel()
		t.Fatal("録画が残る間に電源動作が実行されました")
	default:
	}
	executor.release <- struct{}{}
	select {
	case <-controller.called:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("全録画完了後に電源動作が実行されませんでした")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if controller.calls != 1 || controller.mode != core.PostRecordingStandby {
		t.Fatalf("controller=%+v", controller)
	}
}

type postPowerDecisionRaceStore struct {
	decision    sync.Mutex
	state       sync.Mutex
	queries     int
	first       core.Reservation
	next        *core.Reservation
	idleRead    chan struct{}
	staleRead   chan struct{}
	releaseRead chan struct{}
}

func (*postPowerDecisionRaceStore) ExpireOneDisabledReservation(context.Context, time.Time) (bool, error) {
	return false, nil
}

func (*postPowerDecisionRaceStore) NextDisabledReservationDeadline(context.Context, time.Time) (*time.Time, error) {
	return nil, nil
}

func (store *postPowerDecisionRaceStore) NextActiveReservation(context.Context, time.Time) (*core.Reservation, error) {
	store.state.Lock()
	store.queries++
	query := store.queries
	var next *core.Reservation
	if query == 1 {
		copied := store.first
		next = &copied
	} else if store.next != nil {
		copied := *store.next
		next = &copied
	}
	store.state.Unlock()
	switch query {
	case 2:
		close(store.idleRead)
	case 3:
		close(store.staleRead)
		<-store.releaseRead
	}
	return next, nil
}

func (store *postPowerDecisionRaceStore) LockPostRecordingPowerDecision() {
	store.decision.Lock()
}

func (store *postPowerDecisionRaceStore) UnlockPostRecordingPowerDecision() {
	store.decision.Unlock()
}

func (store *postPowerDecisionRaceStore) add(reservation core.Reservation) {
	store.LockPostRecordingPowerDecision()
	defer store.UnlockPostRecordingPowerDecision()
	store.state.Lock()
	defer store.state.Unlock()
	store.next = &reservation
}

func TestSchedulerRechecksReservationAfterPowerDecisionRace(t *testing.T) {
	now := time.Date(2026, 8, 10, 3, 0, 0, 0, time.UTC)
	first := reservationForExecutor(t, now, 10*time.Minute)
	first.PostRecording.Mode = core.PostRecordingStandby
	store := &postPowerDecisionRaceStore{
		first: first, idleRead: make(chan struct{}), staleRead: make(chan struct{}), releaseRead: make(chan struct{}),
	}
	executor := &concurrentSchedulerExecutor{started: make(chan core.Reservation, 1), release: make(chan struct{}, 1)}
	scheduler, err := NewScheduler(store, executor, &manualScheduleClock{now: now}, 1)
	if err != nil {
		t.Fatal(err)
	}
	controller := &postPowerRecorder{called: make(chan struct{}, 1)}
	reasons := make(chan string, 1)
	if err := scheduler.SetPostRecordingPower(controller, func(reason string) { reasons <- reason }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	waitSchedulerStart(t, executor.started)
	<-store.idleRead
	executor.release <- struct{}{}
	<-store.staleRead
	store.add(reservationForExecutor(t, now.Add(5*time.Minute), 10*time.Minute))
	scheduler.Notify()
	close(store.releaseRead)
	select {
	case reason := <-reasons:
		if reason != "post-recording-power-too-late" {
			cancel()
			t.Fatalf("reason=%s", reason)
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("追加予約を使った電源判断が行われませんでした")
	}
	select {
	case <-controller.called:
		cancel()
		t.Fatal("直前に追加した予約を見落として電源動作が実行されました")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitSchedulerStart(t *testing.T, started <-chan core.Reservation) core.Reservation {
	t.Helper()
	select {
	case reservation := <-started:
		return reservation
	case <-time.After(5 * time.Second):
		t.Fatal("録画処理が開始されませんでした")
		return core.Reservation{}
	}
}
