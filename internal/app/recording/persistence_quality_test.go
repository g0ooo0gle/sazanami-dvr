package recording

import (
	"context"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestQualityExecutorPersistsFinalSummary(t *testing.T) {
	for _, shortWrite := range []bool{false, true} {
		start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		clock := &mutableClock{now: start}
		store := &attemptMemory{start: start, end: start.Add(time.Minute)}
		packet := makePayloadPacket(0x101)
		packet[1] |= 0x80
		lease := &fakeLease{read: func(destination []byte) (int, stream.Terminal, error) {
			copy(destination, packet)
			clock.now = store.end
			return 188, stream.Terminal{Reason: stream.TerminalActive}, nil
		}}
		executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, shortWrite)
		if _, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute)); err != nil {
			t.Fatal(err)
		}
		if store.finish.Quality.Status != core.QualityDegraded || store.finish.Quality.TEIPackets != 1 {
			t.Fatalf("short=%t 最終品質を落としました: %+v", shortWrite, store.finish)
		}
	}
}

func TestQualityRecoveryKeepsWarnings(t *testing.T) {
	for _, planned := range []bool{false, true} {
		for _, status := range []core.QualityStatus{core.QualityUnknown, core.QualityNoIssuesObserved, core.QualityDegraded} {
			state := core.AttemptRecording
			if planned {
				state = core.AttemptFinalizing
			}
			memory := recoveryMemory{item: recoveryItem(t, state), observation: core.FileObservation{Partial: regularFact(376)}}
			q := core.QualitySummary{Status: status, ObservationLimited: true}
			if status == core.QualityDegraded {
				q.CCGapEvents = 4
			}
			memory.item.Quality = q
			if planned {
				memory.item.FinalizationToken = appID(t, 60)
				memory.item.FileSynced = true
			}
			if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := q
			if !planned && status != core.QualityDegraded {
				want.Status = core.QualityUnknown
			}
			if len(memory.finish) != 1 || memory.finish[0].Quality != want {
				t.Fatalf("planned=%t status=%v 品質を失いました: %+v", planned, status, memory.finish)
			}
		}
	}
}

func TestQualityReconciliationIncludesOnlyPlannedCommunication(t *testing.T) {
	for _, planned := range []bool{false, true} {
		item := recoveryItem(t, core.AttemptPartial)
		item.ByteCount, item.SegmentState = 376, core.SegmentFinalized
		item.Availability, item.FileSynced, item.FinalPublished, item.DirectorySynced = core.AvailabilityFinal, true, true, true
		if planned {
			item.FinalizationToken, item.PlannedState, item.PlannedReason = appID(t, 60), core.AttemptPartial, core.ReasonStreamTimeout
		}
		memory := &recoveryMemory{item: item}
		reconciler := CompletedReconciler{Store: memory, Clock: &mutableClock{now: item.PlannedEnd},
			Inspect: func(core.FilePlan) (core.FileObservation, error) { return core.FileObservation{}, nil }}
		result, _, err := reconciler.Run(context.Background())
		want := 0
		if planned {
			want = 1
		}
		if err != nil || result.Checked != want || result.Changed != want {
			t.Fatalf("planned=%t result=%+v err=%v", planned, result, err)
		}
	}
}

func TestQualityPreRecordingFailureRetainsSummary(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	executor := executorForTest(t, store, &fakeProvider{}, &mutableClock{now: start}, false)
	q := core.QualitySummary{Status: core.QualityDegraded, ReconnectCount: 3}
	var operations []string
	_, err := executor.finishStreamFailure(context.Background(), &fakePartial{operations: &operations}, appID(t, 20), 0,
		core.ReasonStreamReconnectExhausted, false, q)
	if err != nil || store.finish.Quality != q {
		t.Fatalf("開始前の品質を落としました: finish=%+v err=%v", store.finish, err)
	}
}
