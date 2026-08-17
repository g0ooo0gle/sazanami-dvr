package recording

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompletedReconcileRunnerStartsImmediatelyWaitsAndNeverOverlaps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	events := make(chan CompletedReconcileEvent, 3)
	done := make(chan error, 1)
	go func() {
		done <- runCompletedReconcile(ctx, 15*time.Millisecond, func(context.Context) (CompletedReconcileResult, string, error) {
			current := active.Add(1)
			if current > maximum.Load() {
				maximum.Store(current)
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			calls.Add(1)
			return CompletedReconcileResult{Checked: 2, Changed: 1, Missing: 1}, "", nil
		}, func(event CompletedReconcileEvent) {
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
		if !event.Completed || event.Checked != 2 || event.Changed != 1 || event.Missing != 1 || event.Mismatched != 0 || event.Reason != "" {
			t.Fatalf("event=%+v", event)
		}
	}
}

func TestCompletedReconcileRunnerContinuesAfterFailureAndBoundsReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	events := make(chan CompletedReconcileEvent, 2)
	done := make(chan error, 1)
	go func() {
		done <- runCompletedReconcile(ctx, time.Millisecond, func(context.Context) (CompletedReconcileResult, string, error) {
			calls++
			if calls == 1 {
				return CompletedReconcileResult{Checked: 9, Changed: 9}, "private value", errors.New("private path")
			}
			return CompletedReconcileResult{Checked: 1, Mismatched: 1}, "", nil
		}, func(event CompletedReconcileEvent) {
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
	if first.Completed || first.Checked != 0 || first.Changed != 0 || first.Reason != "recording-reconcile-internal" {
		t.Fatalf("first=%+v", first)
	}
	if !second.Completed || second.Checked != 1 || second.Mismatched != 1 || second.Reason != "" {
		t.Fatalf("second=%+v", second)
	}
}

func TestCompletedReconcileRunnerUsesOneMinuteAndStopsCanceledOperation(t *testing.T) {
	if CompletedReconcileInterval != time.Minute {
		t.Fatalf("interval=%s", CompletedReconcileInterval)
	}
	runner := CompletedReconcileRunner{
		Reconcile: func(context.Context) (CompletedReconcileResult, string, error) {
			return CompletedReconcileResult{}, "", nil
		},
		Observe: func(CompletedReconcileEvent) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (CompletedReconcileRunner{}).Run(context.Background()); err == nil {
		t.Fatal("invalid runner was accepted")
	}
}
