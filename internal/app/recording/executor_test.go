package recording

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type mutableClock struct{ now time.Time }

func (clock *mutableClock) Now() time.Time { return clock.now }

type attemptMemory struct {
	start      time.Time
	end        time.Time
	claim      core.ClaimRequest
	finish     core.FinishRequest
	progress   []int64
	operations []string
	finishErr  error
}

func (store *attemptMemory) ClaimRecording(_ context.Context, request core.ClaimRequest) (core.Attempt, error) {
	store.claim = request
	store.operations = append(store.operations, "claim")
	return core.Attempt{
		ID: request.AttemptID, ReservationID: request.ReservationID, State: core.AttemptClaimed,
		PlannedStart: store.start, PlannedEnd: store.end, Plan: request.Plan,
	}, nil
}

func (store *attemptMemory) StartAttempt(context.Context, catalogmodel.ID, time.Time) error {
	store.operations = append(store.operations, "starting")
	return nil
}

func (store *attemptMemory) RecordingStarted(context.Context, catalogmodel.ID, time.Time) error {
	store.operations = append(store.operations, "recording")
	return nil
}

func (store *attemptMemory) UpdateRecordingProgress(_ context.Context, _ catalogmodel.ID, count int64, _ time.Time) error {
	store.progress = append(store.progress, count)
	store.operations = append(store.operations, "progress")
	return nil
}

func (store *attemptMemory) BeginFinalization(context.Context, core.FinalizeRequest) error {
	store.operations = append(store.operations, "finalizing")
	return nil
}

func (store *attemptMemory) MarkFinalPublished(context.Context, catalogmodel.ID, time.Time) error {
	store.operations = append(store.operations, "published")
	return nil
}

func (store *attemptMemory) MarkDirectorySynced(context.Context, catalogmodel.ID, time.Time) error {
	store.operations = append(store.operations, "directory-recorded")
	return nil
}

func (store *attemptMemory) FinishAttempt(_ context.Context, request core.FinishRequest) error {
	store.finish = request
	store.operations = append(store.operations, "finished")
	return store.finishErr
}

type fakePartial struct {
	operations *[]string
	short      bool
}

func (file *fakePartial) Write(data []byte) (int, error) {
	*file.operations = append(*file.operations, "write")
	if file.short {
		return len(data) - 1, nil
	}
	return len(data), nil
}

func (file *fakePartial) Sync() error {
	*file.operations = append(*file.operations, "file-sync")
	return nil
}

func (file *fakePartial) Close() error {
	*file.operations = append(*file.operations, "file-close")
	return nil
}

type fakeProvider struct {
	lease   providerstream.Lease
	err     error
	opens   int
	request providerstream.Request
}

func (stream *fakeProvider) OpenStream(_ context.Context, request providerstream.Request) (providerstream.Lease, error) {
	stream.opens++
	stream.request = request
	return stream.lease, stream.err
}

type fakeLease struct {
	read   func([]byte) (int, providerstream.Terminal, error)
	cancel int
	close  int
}

func (lease *fakeLease) Read(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
	return lease.read(destination)
}

func (lease *fakeLease) Cancel() error { lease.cancel++; return nil }
func (lease *fakeLease) Close() error  { lease.close++; return nil }

func TestExecutorPublishesOnlyAfterPlannedEnd(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	lease := &fakeLease{}
	lease.read = func(destination []byte) (int, providerstream.Terminal, error) {
		if len(destination) != provider.MaxStreamChunk {
			t.Fatalf("buffer=%d", len(destination))
		}
		for index := 0; index < 188; index++ {
			destination[index] = 0x47
		}
		clock.now = store.end
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}
	stream := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, stream, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptSucceeded || result.Reason != core.ReasonCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{"claim", "starting", "create", "recording", "write", "progress", "file-sync", "file-close",
		"finalizing", "link", "published", "directory-sync", "remove", "directory-sync", "directory-recorded", "finished"}
	if !equalStrings(store.operations, want) {
		t.Fatalf("operations=%v", store.operations)
	}
	if store.finish.ByteCount != 188 || store.finish.Availability != core.AvailabilityFinal ||
		stream.opens != 1 || stream.request.Target.Opaque != "1003" || stream.request.PriorityPolicy != "0" ||
		!stream.request.RequireDescrambled || lease.cancel != 1 || lease.close != 1 {
		t.Fatalf("finish=%+v opens=%d request=%+v cancel=%d close=%d",
			store.finish, stream.opens, stream.request, lease.cancel, lease.close)
	}
}

func TestExecutorKeepsEarlyStreamAsPartial(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		for index := 0; index < 188; index++ {
			destination[index] = 0x47
		}
		return 188, providerstream.Terminal{Done: true, Reason: providerstream.TerminalEarlyEOF},
			provider.NewFailure(provider.ReasonEarlyEOF, "test")
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptPartial || result.Reason != core.ReasonStreamEndedEarly ||
		store.finish.ByteCount != 188 || store.finish.Availability != core.AvailabilityPartial {
		t.Fatalf("result=%+v finish=%+v err=%v", result, store.finish, err)
	}
	for _, operation := range store.operations {
		if operation == "link" || operation == "finalizing" {
			t.Fatalf("途中終了で完成処理を開始しました: %v", store.operations)
		}
	}
}

func TestExecutorDoesNotTreatShortWriteAsUsefulRecording(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		return 100, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, true)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonFileWriteFailed ||
		store.finish.ByteCount != 99 || store.finish.Availability != core.AvailabilityPartial {
		t.Fatalf("result=%+v finish=%+v err=%v", result, store.finish, err)
	}
}

func TestExecutorRecordsOpenTimeoutWithoutRetry(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	stream := &fakeProvider{err: provider.NewFailure(provider.ReasonTimeout, "test")}
	executor := executorForTest(t, store, stream, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonStreamTimeout || stream.opens != 1 ||
		store.finish.Availability != core.AvailabilityPartial {
		t.Fatalf("result=%+v opens=%d err=%v", result, stream.opens, err)
	}
}

func TestExecutorSettlesShutdownWithoutPublishingFinalFile(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Hour)}
	lease := &contextLease{started: make(chan struct{})}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := executor.Execute(ctx, reservationForExecutor(t, start, time.Hour))
		done <- struct {
			result Result
			err    error
		}{result: result, err: err}
	}()
	select {
	case <-lease.started:
	case <-time.After(time.Second):
		t.Fatal("stream readが始まりませんでした")
	}
	cancel()
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.State != core.AttemptCancelled ||
			outcome.result.Reason != core.ReasonProcessShutdown || store.finish.Availability != core.AvailabilityPartial {
			t.Fatalf("result=%+v finish=%+v err=%v", outcome.result, store.finish, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown後に録画処理が終了しませんでした")
	}
	for _, operation := range store.operations {
		if operation == "link" || operation == "finalizing" {
			t.Fatalf("shutdownした録画を完成扱いにしました: %v", store.operations)
		}
	}
}

func TestExecutorDoesNotReportSuccessWhenFinalDatabaseCommitFails(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute), finishErr: errors.New("database failed")}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, 188))
		clock.now = store.end
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err == nil || result.State != core.AttemptFinalizing || result.Reason != core.ReasonFinalDatabaseFailed ||
		store.finish.State != core.AttemptSucceeded {
		t.Fatalf("result=%+v finish=%+v err=%v", result, store.finish, err)
	}
}

type contextLease struct{ started chan struct{} }

func (lease *contextLease) Read(ctx context.Context, _ []byte) (int, providerstream.Terminal, error) {
	close(lease.started)
	<-ctx.Done()
	return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled},
		provider.NewFailure(provider.ReasonCancelled, "test")
}

func (*contextLease) Cancel() error { return nil }
func (*contextLease) Close() error  { return nil }

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func executorForTest(t *testing.T, store *attemptMemory, stream *fakeProvider, clock *mutableClock, short bool) Executor {
	t.Helper()
	ids := []catalogmodel.ID{appID(t, 20), appID(t, 21), appID(t, 22)}
	index := 0
	partial := &fakePartial{operations: &store.operations, short: short}
	return Executor{
		Store: store, Stream: stream, Clock: clock, OwnerID: appID(t, 23), Generation: 1,
		NewID: func() (catalogmodel.ID, error) {
			if index >= len(ids) {
				return catalogmodel.ID{}, errors.New("too many ids")
			}
			id := ids[index]
			index++
			return id, nil
		},
		Files: FileOperations{
			CreatePartial: func(core.FilePlan) (PartialFile, error) {
				store.operations = append(store.operations, "create")
				return partial, nil
			},
			LinkFinal: func(core.FilePlan) error {
				store.operations = append(store.operations, "link")
				return nil
			},
			SyncDirectory: func(core.FilePlan) error {
				store.operations = append(store.operations, "directory-sync")
				return nil
			},
			RemovePartial: func(core.FilePlan) error {
				store.operations = append(store.operations, "remove")
				return nil
			},
		},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
	}
}

func reservationForExecutor(t *testing.T, start time.Time, duration time.Duration) core.Reservation {
	t.Helper()
	return core.Reservation{
		ID: appID(t, 30), Version: 1, State: core.ReservationActive, Priority: 3,
		Program: core.ProgramSnapshot{
			ProgramInstanceID: appID(t, 31), ProgramRevisionID: appID(t, 32), BackendID: appID(t, 33),
			ProviderServiceLocator: "1003", TuningTarget: "1003", Start: start, Duration: duration,
		},
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
