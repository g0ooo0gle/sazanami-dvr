package recording

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type recoveryMemory struct {
	item               core.RecoveryItem
	observation        core.FileObservation
	oneSegObservation  core.FileObservation
	finish             []core.FinishRequest
	availability       []core.Availability
	oneSegAvailability []core.Availability
	oneSegOutcomes     []core.OneSegResult
	operations         []string
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

func (memory *recoveryMemory) MarkOneSegFinalPublished(context.Context, catalogmodel.ID, time.Time) error {
	memory.operations = append(memory.operations, "one-published")
	memory.item.OneSeg.FinalPublished = true
	return nil
}

func (memory *recoveryMemory) MarkOneSegDirectorySynced(context.Context, catalogmodel.ID, time.Time) error {
	memory.operations = append(memory.operations, "one-directory-recorded")
	memory.item.OneSeg.State = core.SegmentFinalized
	memory.item.OneSeg.Availability = core.AvailabilityFinal
	memory.item.OneSeg.DirectorySynced = true
	return nil
}

func (memory *recoveryMemory) SetOneSegOutcome(_ context.Context, _ catalogmodel.ID, result core.OneSegResult,
	_ time.Time,
) error {
	memory.operations = append(memory.operations, "one-outcome")
	memory.oneSegOutcomes = append(memory.oneSegOutcomes, result)
	memory.item.OneSeg.State = core.SegmentPartial
	memory.item.OneSeg.ByteCount = result.ByteCount
	memory.item.OneSeg.FileSynced = result.FileSynced
	memory.item.OneSeg.Availability = result.Availability
	memory.item.OneSeg.IntegrityReason = result.Reason
	return nil
}

func (memory *recoveryMemory) FinishAttempt(_ context.Context, request core.FinishRequest) error {
	memory.finish = append(memory.finish, request)
	memory.item.State = request.State
	memory.item.Availability = request.Availability
	return nil
}

func (memory *recoveryMemory) SetRecordingAvailability(_ context.Context, _ catalogmodel.ID,
	availability core.Availability, reason core.TerminalReason, _ time.Time,
) error {
	memory.availability = append(memory.availability, availability)
	memory.item.Availability = availability
	memory.item.IntegrityReason = reason
	return nil
}

func (memory *recoveryMemory) SetOneSegAvailability(_ context.Context, _ catalogmodel.ID,
	availability core.Availability, reason core.TerminalReason, _ time.Time,
) error {
	memory.oneSegAvailability = append(memory.oneSegAvailability, availability)
	memory.item.OneSeg.Availability = availability
	memory.item.OneSeg.IntegrityReason = reason
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

func TestCompletedAvailabilityRejectsUnsafeMissingFile(t *testing.T) {
	observation := core.FileObservation{Unsafe: true}
	availability, reason := completedAvailability(376, observation)
	if availability != core.AvailabilityMismatched || reason != core.ReasonFileIntegrityMismatch {
		t.Fatalf("availability=%s reason=%s", availability, reason)
	}
	segment := core.RecoverySegment{
		State: core.SegmentFinalized, Availability: core.AvailabilityFinal, ByteCount: 376,
	}
	availability, reason = settledOneSegAvailability(segment, observation)
	if availability != core.AvailabilityMismatched || reason != core.ReasonFileIntegrityMismatch {
		t.Fatalf("one_seg_availability=%s reason=%s", availability, reason)
	}
}

func TestCompletedAvailabilityDistinguishesMissingAndLeftoverPartial(t *testing.T) {
	availability, reason := completedAvailability(376, core.FileObservation{})
	if availability != core.AvailabilityMissing || reason != core.ReasonFileMissing {
		t.Fatalf("missing availability=%s reason=%s", availability, reason)
	}
	availability, reason = completedAvailability(376, core.FileObservation{Partial: regularFact(376)})
	if availability != core.AvailabilityMismatched || reason != core.ReasonFileIntegrityMismatch {
		t.Fatalf("partial availability=%s reason=%s", availability, reason)
	}
}

func TestRecoveryFinalizesMainBeforeOneSeg(t *testing.T) {
	item := recoveryItem(t, core.AttemptFinalizing)
	item.FileSynced = true
	item.FinalizationToken = appID(t, 56)
	addOneSegRecovery(t, &item, core.SegmentPartial, core.AvailabilityPartial)
	item.OneSeg.FileSynced = true
	memory := recoveryMemory{
		item: item, observation: core.FileObservation{Partial: regularFact(376)},
		oneSegObservation: core.FileObservation{Partial: regularFact(376)},
	}
	if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"link", "published", "directory-sync", "remove", "directory-sync", "directory-recorded",
		"one-link", "one-published", "one-directory-sync", "one-remove", "one-directory-sync", "one-directory-recorded",
	}
	if !equalStrings(memory.operations, want) || len(memory.finish) != 1 ||
		memory.finish[0].State != core.AttemptSucceeded || memory.finish[0].OneSeg != nil {
		t.Fatalf("operations=%v finish=%+v", memory.operations, memory.finish)
	}
}

func TestRecoveryFinalizesMainBeforeInspectingOneSeg(t *testing.T) {
	item := recoveryItem(t, core.AttemptFinalizing)
	item.FileSynced = true
	item.FinalizationToken = appID(t, 59)
	addOneSegRecovery(t, &item, core.SegmentPartial, core.AvailabilityPartial)
	item.OneSeg.FileSynced = true
	memory := recoveryMemory{
		item: item, observation: core.FileObservation{Partial: regularFact(376)},
		oneSegObservation: core.FileObservation{Partial: regularFact(376)},
	}
	recovery := recoveryForTest(&memory)
	recovery.Files.Inspect = func(plan core.FilePlan) (core.FileObservation, error) {
		if plan == memory.item.OneSeg.Plan {
			memory.operations = append(memory.operations, "one-inspect")
			return core.FileObservation{}, errors.New("test")
		}
		memory.operations = append(memory.operations, "inspect")
		return memory.observation, nil
	}
	if err := recovery.Run(context.Background()); err == nil || err.Error() != "recording: inspect one-seg recovery file" {
		t.Fatalf("err=%v", err)
	}
	want := []string{
		"inspect", "link", "published", "directory-sync", "remove", "directory-sync", "directory-recorded",
		"one-inspect",
	}
	if !equalStrings(memory.operations, want) || len(memory.finish) != 0 || !memory.item.DirectorySynced {
		t.Fatalf("operations=%v finish=%+v directory_synced=%t",
			memory.operations, memory.finish, memory.item.DirectorySynced)
	}
}

func TestRecoveryKeepsCompletedMainWhenOneSegIsMissing(t *testing.T) {
	item := recoveryItem(t, core.AttemptFinalizing)
	item.FileSynced = true
	item.FinalPublished = true
	item.DirectorySynced = true
	item.SegmentState = core.SegmentFinalized
	item.Availability = core.AvailabilityFinal
	item.FinalizationToken = appID(t, 57)
	addOneSegRecovery(t, &item, core.SegmentPartial, core.AvailabilityPartial)
	item.OneSeg.FileSynced = true
	memory := recoveryMemory{item: item, observation: core.FileObservation{Final: regularFact(376)}}
	if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(memory.operations, []string{"directory-sync", "one-outcome"}) ||
		len(memory.oneSegOutcomes) != 1 || memory.oneSegOutcomes[0].Availability != core.AvailabilityMissing ||
		memory.oneSegOutcomes[0].Reason != core.ReasonFileMissing || len(memory.finish) != 1 ||
		memory.finish[0].State != core.AttemptSucceeded {
		t.Fatalf("operations=%v oneSeg=%+v finish=%+v", memory.operations, memory.oneSegOutcomes, memory.finish)
	}
	if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(memory.oneSegOutcomes) != 1 || len(memory.finish) != 1 {
		t.Fatalf("二回目の復旧で状態が増えました: oneSeg=%+v finish=%+v", memory.oneSegOutcomes, memory.finish)
	}
}

func TestRecoverySettlesInterruptedOneSegWithMain(t *testing.T) {
	item := recoveryItem(t, core.AttemptRecording)
	addOneSegRecovery(t, &item, core.SegmentWriting, core.AvailabilityPlanned)
	memory := recoveryMemory{
		item: item, observation: core.FileObservation{Partial: regularFact(376)},
		oneSegObservation: core.FileObservation{Partial: regularFact(188)},
	}
	if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(memory.finish) != 1 || memory.finish[0].State != core.AttemptPartial || memory.finish[0].OneSeg == nil ||
		memory.finish[0].OneSeg.ByteCount != 188 || memory.finish[0].OneSeg.Availability != core.AvailabilityPartial ||
		memory.finish[0].OneSeg.Reason != core.ReasonProcessInterrupted || len(memory.operations) != 0 {
		t.Fatalf("finish=%+v operations=%v", memory.finish, memory.operations)
	}
}

func TestRecoveryReconcilesCompletedOneSegWithoutChangingMain(t *testing.T) {
	item := recoveryItem(t, core.AttemptSucceeded)
	item.FileSynced = true
	item.FinalPublished = true
	item.DirectorySynced = true
	item.FinalizationToken = appID(t, 58)
	addOneSegRecovery(t, &item, core.SegmentFinalized, core.AvailabilityFinal)
	item.OneSeg.FileSynced = true
	item.OneSeg.FinalPublished = true
	item.OneSeg.DirectorySynced = true
	memory := recoveryMemory{item: item, observation: core.FileObservation{Final: regularFact(376)}}
	if err := recoveryForTest(&memory).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(memory.availability) != 0 || len(memory.oneSegAvailability) != 1 ||
		memory.oneSegAvailability[0] != core.AvailabilityMissing || len(memory.finish) != 0 {
		t.Fatalf("main=%v oneSeg=%v finish=%v", memory.availability, memory.oneSegAvailability, memory.finish)
	}
}

func recoveryForTest(memory *recoveryMemory) Recovery {
	oneSegPlan := func(plan core.FilePlan) bool {
		return memory.item.OneSeg != nil && plan == memory.item.OneSeg.Plan
	}
	operation := func(plan core.FilePlan, name string) {
		if oneSegPlan(plan) {
			name = "one-" + name
		}
		memory.operations = append(memory.operations, name)
	}
	observation := func(plan core.FilePlan) *core.FileObservation {
		if oneSegPlan(plan) {
			return &memory.oneSegObservation
		}
		return &memory.observation
	}
	return Recovery{
		Store: memory, Clock: &mutableClock{now: time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)},
		Files: RecoveryFiles{
			FileOperations: FileOperations{
				CreatePartial: func(core.FilePlan) (PartialFile, error) { return nil, nil },
				LinkFinal: func(plan core.FilePlan) error {
					operation(plan, "link")
					current := observation(plan)
					current.Final = current.Partial
					current.SameFile = true
					return nil
				},
				SyncDirectory: func(plan core.FilePlan) error {
					operation(plan, "directory-sync")
					return nil
				},
				RemovePartial: func(plan core.FilePlan) error {
					operation(plan, "remove")
					current := observation(plan)
					current.Partial = core.FileFact{}
					current.SameFile = false
					return nil
				},
			},
			Inspect: func(plan core.FilePlan) (core.FileObservation, error) { return *observation(plan), nil },
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
	item.SegmentState = core.SegmentPlanned
	item.Availability = core.AvailabilityPlanned
	if state == core.AttemptStarting || state == core.AttemptRecording {
		item.SegmentState = core.SegmentWriting
	}
	if state == core.AttemptFinalizing || state == core.AttemptSucceeded {
		item.PlannedState = core.AttemptSucceeded
		item.PlannedReason = core.ReasonCompleted
		item.SegmentState = core.SegmentPartial
		item.Availability = core.AvailabilityPartial
	}
	if state == core.AttemptSucceeded {
		item.SegmentState = core.SegmentFinalized
		item.Availability = core.AvailabilityFinal
	}
	return item
}

func addOneSegRecovery(t *testing.T, item *core.RecoveryItem, state core.SegmentState,
	availability core.Availability,
) {
	t.Helper()
	plan, err := core.NewFilePlan(item.PlannedStart, appID(t, 55))
	if err != nil {
		t.Fatal(err)
	}
	item.OneSeg = &core.RecoverySegment{
		Plan: plan, ByteCount: 376, State: state, Availability: availability,
	}
	item.OneSegPlan = &plan
}

func regularFact(size int64) core.FileFact {
	return core.FileFact{Exists: true, Regular: true, Size: size}
}
