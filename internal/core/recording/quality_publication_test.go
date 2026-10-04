package recording

import (
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestQualityFinalizationPlanReasons(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		reason  TerminalReason
		allowed bool
	}{
		{ReasonStreamEndedEarly, true}, {ReasonStreamTimeout, true}, {ReasonStreamReconnectExhausted, true},
		{ReasonStreamUnavailable, false}, {ReasonProcessShutdown, false}, {ReasonFileWriteFailed, false},
	} {
		if IsCommunicationPartialReason(test.reason) != test.allowed {
			t.Fatalf("公開reasonの判定が不一致です: %s", test.reason)
		}
		for _, count := range []int64{0, 187, 188, 189, 376} {
			request := FinalizeRequest{AttemptID: idForTest(t, 1), Token: idForTest(t, 2), ByteCount: count,
				State: AttemptPartial, Reason: test.reason, Now: now, Quality: QualitySummary{Status: QualityDegraded}}
			want := test.allowed && (count == 188 || count == 376)
			if (request.Validate() == nil) != want {
				t.Fatalf("reason=%s bytes=%d finalize=%v", test.reason, count, request.Validate())
			}
			finish := FinishRequest{AttemptID: request.AttemptID, State: request.State, Reason: request.Reason,
				ByteCount: count, Availability: AvailabilityFinal, Now: now, Quality: request.Quality}
			if (finish.Validate() == nil) != want {
				t.Fatalf("reason=%s bytes=%d finish=%v", test.reason, count, finish.Validate())
			}
		}
	}
}

func TestQualityCommunicationHistoryRequiresNewPlanProof(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	for _, duration := range []time.Duration{999 * time.Millisecond, time.Second} {
		end := start.Add(duration)
		for _, count := range []int64{0, 187, 188, 189, 376} {
			item := HistoryItem{Number: 1, State: AttemptPartial, Reason: ReasonStreamTimeout,
				PlannedStart: start, PlannedEnd: start.Add(time.Hour), ActualStart: &start, ActualEnd: &end,
				ByteCount: count, Plan: FilePlan{PartialPath: "a.ts.partial", FinalPath: "a.ts"},
				SegmentState: SegmentFinalized, Availability: AvailabilityFinal,
				FileSynced: true, FinalPublished: true, DirectorySynced: true,
				FinalizationToken: idForTest(t, 3), PlannedState: AttemptPartial, PlannedReason: ReasonStreamTimeout}
			want := duration == time.Second && (count == 188 || count == 376)
			if item.Playable() != want {
				t.Fatalf("duration=%s bytes=%d playable=%t", duration, count, item.Playable())
			}
			if !want {
				continue
			}
			for _, change := range []func(*HistoryItem){
				func(v *HistoryItem) { v.FinalizationToken = catalogmodel.ID{} },
				func(v *HistoryItem) { v.PlannedState = AttemptSucceeded },
				func(v *HistoryItem) { v.PlannedReason = ReasonStreamEndedEarly },
				func(v *HistoryItem) { v.FileSynced = false },
				func(v *HistoryItem) { v.FinalPublished = false },
				func(v *HistoryItem) { v.DirectorySynced = false },
			} {
				bad := item
				change(&bad)
				if bad.Playable() {
					t.Fatal("証跡が欠けた通信partialが公開されました")
				}
			}
			legacy := item
			legacy.State, legacy.Reason = AttemptSucceeded, ReasonCompleted
			legacy.FinalizationToken, legacy.PlannedState, legacy.PlannedReason = catalogmodel.ID{}, "", ""
			legacy.ByteCount = 189
			if !legacy.Playable() {
				t.Fatal("旧完録へ新しい公開条件が遡及しました")
			}
		}
	}
}
