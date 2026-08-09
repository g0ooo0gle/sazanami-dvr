package catalogrefresh

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunStartsImmediatelyWaitsAfterCompletionAndDoesNotOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	events := make(chan Event, 3)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, 15*time.Millisecond, time.Second, func(context.Context) (Result, string, error) {
			current := active.Add(1)
			if current > maximum.Load() {
				maximum.Store(current)
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			calls.Add(1)
			return Result{Services: 2, Programs: 3}, "", nil
		}, func(event Event) {
			events <- event
			if len(events) == 3 {
				cancel()
			}
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	if calls.Load() != 3 || maximum.Load() != 1 {
		t.Fatalf("calls=%d maximum=%d", calls.Load(), maximum.Load())
	}
	for range 3 {
		event := <-events
		if !event.Completed || event.Services != 2 || event.Programs != 3 || event.Reason != "" {
			t.Fatalf("event=%+v", event)
		}
	}
}

func TestRunContinuesAfterFailureAndBoundsReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	events := make(chan Event, 2)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, time.Millisecond, time.Second, func(context.Context) (Result, string, error) {
			calls++
			if calls == 1 {
				return Result{Services: 9, Programs: 9}, "private value", errors.New("failed")
			}
			return Result{Services: 1, Programs: 4}, "", nil
		}, func(event Event) {
			events <- event
			if len(events) == 2 {
				cancel()
			}
		})
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	first, second := <-events, <-events
	if first.Completed || first.Services != 0 || first.Programs != 0 || first.Reason != "catalog-refresh-internal" {
		t.Fatalf("first=%+v", first)
	}
	if !second.Completed || second.Services != 1 || second.Programs != 4 {
		t.Fatalf("second=%+v", second)
	}
}

func TestRunnerRejectsUnsafeIntervalAndStopsCanceledOperation(t *testing.T) {
	valid := Runner{Interval: MinimumInterval, Sync: func(context.Context) (Result, string, error) {
		return Result{}, "", nil
	}, Observe: func(Event) {}}
	for _, interval := range []time.Duration{0, MinimumInterval - 1, MaximumInterval + 1} {
		invalid := valid
		invalid.Interval = interval
		if err := invalid.Run(context.Background()); err == nil {
			t.Fatalf("interval=%s was accepted", interval)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := valid.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
