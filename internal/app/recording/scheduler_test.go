package recording

import (
	"context"
	"sync"
	"testing"
	"time"

	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type queueStore struct {
	mu      sync.Mutex
	items   []core.Reservation
	queries int
	queried chan struct{}
}

func (store *queueStore) NextActiveReservation(context.Context) (*core.Reservation, error) {
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
	clock     *manualScheduleClock
	execute   []core.Reservation
	miss      []core.TerminalReason
	onExecute func()
	executed  chan struct{}
}

func (executor *schedulerExecutor) Execute(_ context.Context, reservation core.Reservation) (Result, error) {
	executor.execute = append(executor.execute, reservation)
	if executor.onExecute != nil {
		executor.onExecute()
	}
	if executor.executed != nil {
		select {
		case executor.executed <- struct{}{}:
		default:
		}
	}
	executor.clock.now = executor.clock.now.Add(2 * time.Minute)
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

func TestSchedulerUsesOneSlotAndLateStartRules(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &manualScheduleClock{now: start}
	first := reservationForExecutor(t, start, 10*time.Minute)
	second := reservationForExecutor(t, start.Add(time.Minute), 10*time.Minute)
	second.ID = appID(t, 40)
	late := reservationForExecutor(t, start.Add(-10*time.Minute), 20*time.Minute)
	late.ID = appID(t, 41)
	store := &queueStore{items: []core.Reservation{first, second, late}, queried: make(chan struct{}, 8)}
	executor := &schedulerExecutor{clock: clock}
	scheduler, err := NewScheduler(store, executor, clock)
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
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(executor.execute) != 1 || len(executor.miss) != 2 ||
		executor.miss[0] != core.ReasonRecordingSlotUnavailable || executor.miss[1] != core.ReasonLateStartExpired {
		t.Fatalf("execute=%d miss=%v", len(executor.execute), executor.miss)
	}
}

func TestSchedulerWaitsForNotificationWithoutPolling(t *testing.T) {
	clock := &manualScheduleClock{now: time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)}
	store := &queueStore{queried: make(chan struct{}, 4)}
	scheduler, err := NewScheduler(store, &schedulerExecutor{clock: clock}, clock)
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

type futureStore struct {
	item core.Reservation
	done bool
}

func (store *futureStore) NextActiveReservation(context.Context) (*core.Reservation, error) {
	if store.done {
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
	executor.onExecute = func() { store.done = true }
	scheduler, err := NewScheduler(store, executor, clock)
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
	if clock.duration != time.Hour-startupLeadTime {
		cancel()
		t.Fatalf("timer=%s", clock.duration)
	}
	clock.now = start.Add(-startupLeadTime)
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
