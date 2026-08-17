package recording

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type reconcileUpdate struct {
	id           catalogmodel.ID
	availability core.Availability
	reason       core.TerminalReason
	oneSeg       bool
}

type reconcileMemory struct {
	items        []core.RecoveryItem
	observations map[core.FilePlan]core.FileObservation
	reads        []catalogmodel.ID
	inspections  []core.FilePlan
	updates      []reconcileUpdate
	readErr      error
	inspectErr   error
	updateErr    error
}

func (memory *reconcileMemory) RecoveryAttempts(ctx context.Context, limit int,
	after catalogmodel.ID,
) ([]core.RecoveryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	memory.reads = append(memory.reads, after)
	if memory.readErr != nil {
		return nil, memory.readErr
	}
	items := make([]core.RecoveryItem, 0, limit)
	for _, item := range memory.items {
		if bytes.Compare(item.ID[:], after[:]) <= 0 {
			continue
		}
		items = append(items, item)
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

func (memory *reconcileMemory) SetRecordingAvailability(_ context.Context, id catalogmodel.ID,
	availability core.Availability, reason core.TerminalReason, _ time.Time,
) error {
	if memory.updateErr != nil {
		return memory.updateErr
	}
	memory.updates = append(memory.updates, reconcileUpdate{id: id, availability: availability, reason: reason})
	return nil
}

func (memory *reconcileMemory) SetOneSegAvailability(_ context.Context, id catalogmodel.ID,
	availability core.Availability, reason core.TerminalReason, _ time.Time,
) error {
	if memory.updateErr != nil {
		return memory.updateErr
	}
	memory.updates = append(memory.updates, reconcileUpdate{
		id: id, availability: availability, reason: reason, oneSeg: true,
	})
	return nil
}

func TestCompletedReconcilerBoundsCursorAndWrap(t *testing.T) {
	for _, count := range []int{99, 100, 101, 999, 1000, 1001} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			memory := &reconcileMemory{observations: make(map[core.FilePlan]core.FileObservation)}
			for number := 1; number <= count; number++ {
				item := completedReconcileItem(t, number, core.AttemptSucceeded)
				item.Availability = core.AvailabilityFinal
				memory.items = append(memory.items, item)
				memory.observations[item.Plan] = core.FileObservation{Final: regularFact(item.ByteCount)}
			}
			reconciler := CompletedReconciler{
				Store: memory, Inspect: memory.inspect,
				Clock: &mutableClock{now: time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)},
			}
			result, reason, err := reconciler.Run(context.Background())
			wantFirst := min(count, MaximumCompletedReconcileItems)
			if err != nil || reason != "" || result.Checked != wantFirst || result.Changed != 0 {
				t.Fatalf("count=%d result=%+v reason=%q err=%v", count, result, reason, err)
			}
			if count < MaximumCompletedReconcileItems && reconciler.after != (catalogmodel.ID{}) {
				t.Fatalf("count=%d cursor=%s", count, reconciler.after)
			}
			if count >= MaximumCompletedReconcileItems {
				result, reason, err = reconciler.Run(context.Background())
				wantSecond := count - MaximumCompletedReconcileItems
				if err != nil || reason != "" || result.Checked != wantSecond || reconciler.after != (catalogmodel.ID{}) {
					t.Fatalf("second result=%+v reason=%q cursor=%s err=%v", result, reason, reconciler.after, err)
				}
			}
		})
	}
}

func TestCompletedReconcilerOnlyChangesTerminalAvailability(t *testing.T) {
	succeeded := completedReconcileItem(t, 1, core.AttemptSucceeded)
	active := completedReconcileItem(t, 2, core.AttemptFinalizing)
	stopped := completedReconcileItem(t, 3, core.AttemptPartial)
	stopped.PlannedState = core.AttemptPartial
	stopped.PlannedReason = core.ReasonUserRequestedStop
	stopped.Availability = core.AvailabilityFinal
	wrongPartial := completedReconcileItem(t, 4, core.AttemptPartial)
	wrongPartial.PlannedState = core.AttemptPartial
	wrongPartial.PlannedReason = core.ReasonProcessInterrupted
	addOneSegRecovery(t, &stopped, core.SegmentFinalized, core.AvailabilityMissing)
	stopped.OneSeg.FileSynced = true
	stopped.OneSeg.FinalPublished = true
	stopped.OneSeg.DirectorySynced = true
	memory := &reconcileMemory{
		items: []core.RecoveryItem{succeeded, active, stopped, wrongPartial},
		observations: map[core.FilePlan]core.FileObservation{
			succeeded.Plan:      {Final: regularFact(succeeded.ByteCount)},
			stopped.Plan:        {},
			stopped.OneSeg.Plan: {Final: regularFact(stopped.OneSeg.ByteCount)},
		},
	}
	reconciler := CompletedReconciler{
		Store: memory, Inspect: memory.inspect,
		Clock: &mutableClock{now: time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)},
	}
	result, reason, err := reconciler.Run(context.Background())
	if err != nil || reason != "" {
		t.Fatalf("result=%+v reason=%q err=%v", result, reason, err)
	}
	if result.Checked != 3 || result.Changed != 3 || result.Missing != 1 || result.Mismatched != 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(memory.inspections) != 3 || len(memory.updates) != 3 {
		t.Fatalf("inspections=%v updates=%v", memory.inspections, memory.updates)
	}
	if update := memory.updates[0]; update.id != succeeded.ID || update.availability != core.AvailabilityFinal || update.reason != "" {
		t.Fatalf("success update=%+v", update)
	}
	if update := memory.updates[1]; update.id != stopped.ID || update.availability != core.AvailabilityMissing || update.reason != core.ReasonFileMissing {
		t.Fatalf("stopped update=%+v", update)
	}
	if update := memory.updates[2]; update.id != stopped.ID || !update.oneSeg || update.availability != core.AvailabilityFinal || update.reason != "" {
		t.Fatalf("one-seg update=%+v", update)
	}
}

func TestCompletedReconcilerKeepsCursorOnFailureAndHonorsCancel(t *testing.T) {
	item := completedReconcileItem(t, 1, core.AttemptSucceeded)
	memory := &reconcileMemory{
		items: []core.RecoveryItem{item}, observations: map[core.FilePlan]core.FileObservation{item.Plan: {}},
		inspectErr: errors.New("private path"),
	}
	reconciler := CompletedReconciler{
		Store: memory, Inspect: memory.inspect,
		Clock: &mutableClock{now: time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)},
	}
	if _, reason, err := reconciler.Run(context.Background()); err == nil || reason != "recording-reconcile-inspect-failed" || reconciler.after != (catalogmodel.ID{}) {
		t.Fatalf("reason=%q cursor=%s err=%v", reason, reconciler.after, err)
	}
	memory.inspectErr = nil
	memory.updateErr = errors.New("private database")
	if _, reason, err := reconciler.Run(context.Background()); err == nil || reason != "recording-reconcile-update-failed" || reconciler.after != (catalogmodel.ID{}) {
		t.Fatalf("reason=%q cursor=%s err=%v", reason, reconciler.after, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, reason, err := reconciler.Run(ctx); !errors.Is(err, context.Canceled) || reason != "recording-reconcile-cancelled" {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
}

func TestCompletedReconcilerRejectsMismatchAndDoesNotPromoteSettledOneSeg(t *testing.T) {
	item := completedReconcileItem(t, 1, core.AttemptSucceeded)
	item.Availability = core.AvailabilityFinal
	addOneSegRecovery(t, &item, core.SegmentPartial, core.AvailabilityPartial)
	item.OneSeg.IntegrityReason = core.ReasonStreamEndedEarly
	memory := &reconcileMemory{
		items: []core.RecoveryItem{item},
		observations: map[core.FilePlan]core.FileObservation{
			item.Plan:        {Unsafe: true},
			item.OneSeg.Plan: {Final: regularFact(item.OneSeg.ByteCount)},
		},
	}
	reconciler := CompletedReconciler{
		Store: memory, Inspect: memory.inspect,
		Clock: &mutableClock{now: time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)},
	}
	result, reason, err := reconciler.Run(context.Background())
	if err != nil || reason != "" || result.Checked != 2 || result.Changed != 2 || result.Mismatched != 2 {
		t.Fatalf("result=%+v reason=%q err=%v", result, reason, err)
	}
	for _, update := range memory.updates {
		if update.availability != core.AvailabilityMismatched || update.reason != core.ReasonFileIntegrityMismatch {
			t.Fatalf("update=%+v", update)
		}
	}
}

func (memory *reconcileMemory) inspect(plan core.FilePlan) (core.FileObservation, error) {
	memory.inspections = append(memory.inspections, plan)
	if memory.inspectErr != nil {
		return core.FileObservation{}, memory.inspectErr
	}
	return memory.observations[plan], nil
}

func completedReconcileItem(t *testing.T, number int, state core.AttemptState) core.RecoveryItem {
	t.Helper()
	id := orderedReconcileID(number)
	start := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	plan, err := core.NewFilePlan(start, id)
	if err != nil {
		t.Fatal(err)
	}
	return core.RecoveryItem{
		Attempt: core.Attempt{
			ID: id, State: state, PlannedStart: start, PlannedEnd: start.Add(time.Hour), ByteCount: 376, Plan: plan,
		},
		PlannedState: core.AttemptSucceeded, PlannedReason: core.ReasonCompleted,
		SegmentState: core.SegmentFinalized, Availability: core.AvailabilityMissing,
	}
}

func orderedReconcileID(number int) catalogmodel.ID {
	var id catalogmodel.ID
	binary.BigEndian.PutUint64(id[8:], uint64(number))
	return id
}
