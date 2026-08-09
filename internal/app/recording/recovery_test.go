package recording

import (
	"context"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type recoveryMemory struct {
	item         core.RecoveryItem
	observation  core.FileObservation
	finish       []core.FinishRequest
	availability []core.Availability
	operations   []string
}

func (memory *recoveryMemory) RecoveryAttempts(_ context.Context, _ int, after catalogmodel.ID) ([]core.RecoveryItem, error) {
	if after != (catalogmodel.ID{}) || memory.item.ID == (catalogmodel.ID{}) {
		return nil, nil
	}
	return []core.RecoveryItem{memory.item}, nil
}

func (memory *recoveryMemory) MarkFinalPublished(context.Context, catalogmodel.ID, time.Time) error {
	memory.operations = append(memory.operations, "published")
	memory.item.FinalPublished = true
	return nil
}

func (memory *recoveryMemory) MarkDirectorySynced(context.Context, catalogmodel.ID, time.Time) error {
	memory.operations = append(memory.operations, "directory-recorded")
	memory.item.DirectorySynced = true
	return nil
}

func (memory *recoveryMemory) FinishAttempt(_ context.Context, request core.FinishRequest) error {
	memory.finish = append(memory.finish, request)
	memory.item.State = request.State
	memory.item.Availability = request.Availability
	return nil
}

func (memory *recoveryMemory) SetRecordingAvailability(_ context.Context, _ catalogmodel.ID, availability core.Availability, _ core.TerminalReason, _ time.Time) error {
	memory.availability = append(memory.availability, availability)
	memory.item.Availability = availability
	return nil
}

func TestRecoverySettlesInterruptedAttemptsWithoutOpeningStream(t *testing.T) {
	tests := []struct {
		name         string
		observation  core.FileObservation
		wantState    core.AttemptState
		wantReason   core.TerminalReason
		wantBytes    int64
		availability core.Availability
	}{
		{name: "partial", observation: core.FileObservation{Partial: regularFact(376)},
			wantState: core.AttemptPartial, wantReason: core.ReasonProcessInterrupted, wantBytes: 376, availability: core.AvailabilityPartial},
		{name: "empty", wantState: core.AttemptFailed, wantReason: core.ReasonProcessInterrupted, availability: core.AvailabilityMissing},
		{name: "empty partial", observation: core.FileObservation{Partial: regularFact(0)},
			wantState: core.AttemptFailed, wantReason: core.ReasonProcessInterrupted, availability: core.AvailabilityPartial},
		{name: "unexpected final", observation: core.FileObservation{Final: regularFact(376)},
			wantState: core.AttemptFailed, wantReason: core.ReasonFileIntegrityMismatch, wantBytes: 376, availability: core.AvailabilityMismatched},
		{name: "unsafe", observation: core.FileObservation{Unsafe: true},
			wantState: core.AttemptFailed, wantReason: core.ReasonFileIntegrityMismatch, wantBytes: 376, availability: core.AvailabilityMismatched},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			memory := recoveryMemory{item: recoveryItem(t, core.AttemptRecording), observation: test.observation}
			recovery := recoveryForTest(&memory)
			if err := recovery.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(memory.finish) != 1 {
				t.Fatalf("finish=%v", memory.finish)
			}
			finish := memory.finish[0]
			if finish.State != test.wantState || finish.Reason != test.wantReason || finish.ByteCount != test.wantBytes ||
				finish.Availability != test.availability || !finish.Recovered || len(memory.operations) != 0 {
				t.Fatalf("finish=%+v operations=%v", finish, memory.operations)
			}
		})
	}
}

func TestRecoveryResumesEverySafeFinalizationWindow(t *testing.T) {
	tests := []struct {
		name          string
		observation   core.FileObservation
		published     bool
		directorySync bool
		want          []string
	}{
		{name: "before link", observation: core.FileObservation{Partial: regularFact(376)},
			want: []string{"link", "published", "directory-sync", "remove", "directory-sync", "directory-recorded"}},
		{name: "after link", observation: core.FileObservation{Partial: regularFact(376), Final: regularFact(376), SameFile: true},
			want: []string{"published", "directory-sync", "remove", "directory-sync", "directory-recorded"}},
		{name: "after publication record", observation: core.FileObservation{Partial: regularFact(376), Final: regularFact(376), SameFile: true},
			published: true, want: []string{"directory-sync", "remove", "directory-sync", "directory-recorded"}},
		{name: "after partial removal", observation: core.FileObservation{Final: regularFact(376)}, published: true,
			want: []string{"directory-sync", "directory-recorded"}},
		{name: "before final database commit", observation: core.FileObservation{Final: regularFact(376)}, published: true,
			directorySync: true, want: []string{"directory-sync"}},
	}
	results := []struct {
		name   string
		state  core.AttemptState
		reason core.TerminalReason
	}{
		{name: "normal", state: core.AttemptSucceeded, reason: core.ReasonCompleted},
		{name: "user stopped", state: core.AttemptPartial, reason: core.ReasonUserRequestedStop},
	}
	for _, final := range results {
		for _, test := range tests {
			t.Run(final.name+"/"+test.name, func(t *testing.T) {
				item := recoveryItem(t, core.AttemptFinalizing)
				item.PlannedState = final.state
				item.PlannedReason = final.reason
				item.FileSynced = true
				item.FinalizationToken = appID(t, 52)
				item.FinalPublished = test.published
				item.DirectorySynced = test.directorySync
				item.Availability = core.AvailabilityPartial
				memory := recoveryMemory{item: item, observation: test.observation}
				if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !equalStrings(memory.operations, test.want) || len(memory.finish) != 1 ||
					memory.finish[0].State != final.state || memory.finish[0].Reason != final.reason || !memory.finish[0].Recovered {
					t.Fatalf("operations=%v finish=%+v", memory.operations, memory.finish)
				}
			})
		}
	}
}

func TestRecoveryRejectsMissingAndMismatchedFinalization(t *testing.T) {
	for _, test := range []struct {
		name         string
		observation  core.FileObservation
		reason       core.TerminalReason
		availability core.Availability
	}{
		{name: "missing", reason: core.ReasonFileMissing, availability: core.AvailabilityMissing},
		{name: "size mismatch", observation: core.FileObservation{Partial: regularFact(188)},
			reason: core.ReasonFileIntegrityMismatch, availability: core.AvailabilityMismatched},
		{name: "different files", observation: core.FileObservation{Partial: regularFact(376), Final: regularFact(376)},
			reason: core.ReasonFileIntegrityMismatch, availability: core.AvailabilityMismatched},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := recoveryItem(t, core.AttemptFinalizing)
			item.FileSynced = true
			item.FinalizationToken = appID(t, 53)
			memory := recoveryMemory{item: item, observation: test.observation}
			if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(memory.finish) != 1 || memory.finish[0].State != core.AttemptFailed ||
				memory.finish[0].Reason != test.reason || memory.finish[0].Availability != test.availability ||
				len(memory.operations) != 0 {
				t.Fatalf("finish=%+v operations=%v", memory.finish, memory.operations)
			}
		})
	}
}

func TestRecoveryReconcilesHistoricalSuccessAndIsIdempotent(t *testing.T) {
	item := recoveryItem(t, core.AttemptSucceeded)
	item.FileSynced = true
	item.FinalPublished = true
	item.DirectorySynced = true
	item.FinalizationToken = appID(t, 54)
	item.Availability = core.AvailabilityFinal
	memory := recoveryMemory{item: item}
	recovery := recoveryForTest(&memory)
	if err := recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(memory.availability) != 1 || memory.availability[0] != core.AvailabilityMissing {
		t.Fatalf("availability=%v", memory.availability)
	}
	if err := recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(memory.availability) != 1 || len(memory.finish) != 0 || len(memory.operations) != 0 {
		t.Fatalf("二回目の復旧で状態が増えました: availability=%v finish=%v operations=%v",
			memory.availability, memory.finish, memory.operations)
	}
}

func recoveryForTest(memory *recoveryMemory) Recovery {
	return Recovery{
		Store: memory, Clock: &mutableClock{now: time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)},
		Files: RecoveryFiles{
			FileOperations: FileOperations{
				CreatePartial: func(core.FilePlan) (PartialFile, error) { return nil, nil },
				LinkFinal: func(core.FilePlan) error {
					memory.operations = append(memory.operations, "link")
					memory.observation.Final = memory.observation.Partial
					memory.observation.SameFile = true
					return nil
				},
				SyncDirectory: func(core.FilePlan) error {
					memory.operations = append(memory.operations, "directory-sync")
					return nil
				},
				RemovePartial: func(core.FilePlan) error {
					memory.operations = append(memory.operations, "remove")
					memory.observation.Partial = core.FileFact{}
					memory.observation.SameFile = false
					return nil
				},
			},
			Inspect: func(core.FilePlan) (core.FileObservation, error) { return memory.observation, nil },
		},
	}
}

func recoveryItem(t *testing.T, state core.AttemptState) core.RecoveryItem {
	t.Helper()
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	id := appID(t, 50)
	plan, err := core.NewFilePlan(start, id)
	if err != nil {
		t.Fatal(err)
	}
	item := core.RecoveryItem{Attempt: core.Attempt{
		ID: id, ReservationID: appID(t, 51), State: state, PlannedStart: start,
		PlannedEnd: start.Add(time.Hour), ByteCount: 376, Plan: plan,
	}}
	if state == core.AttemptFinalizing || state == core.AttemptSucceeded {
		item.PlannedState = core.AttemptSucceeded
		item.PlannedReason = core.ReasonCompleted
	}
	return item
}

func regularFact(size int64) core.FileFact {
	return core.FileFact{Exists: true, Regular: true, Size: size}
}
