package recording

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
	mu           sync.Mutex
	start        time.Time
	end          time.Time
	startEnd     time.Time
	claim        core.ClaimRequest
	finish       core.FinishRequest
	finalize     core.FinalizeRequest
	progress     []int64
	progressEnd  time.Time
	operations   []string
	finishErr    error
	progressErr  error
	finalizeErr  error
	publishErr   error
	directoryErr error
	stop         atomic.Bool
	onOperation  func(string)
}

func (store *attemptMemory) record(operation string) {
	store.mu.Lock()
	store.operations = append(store.operations, operation)
	callback := store.onOperation
	store.mu.Unlock()
	if callback != nil {
		callback(operation)
	}
}

func (store *attemptMemory) ClaimRecording(_ context.Context, request core.ClaimRequest) (core.Attempt, error) {
	store.claim = request
	store.record("claim")
	return core.Attempt{
		ID: request.AttemptID, ReservationID: request.ReservationID, State: core.AttemptClaimed,
		PlannedStart: store.start, PlannedEnd: store.end, Plan: request.Plan, OneSegPlan: request.OneSegPlan,
	}, nil
}

func (store *attemptMemory) StartAttempt(context.Context, catalogmodel.ID, time.Time) error {
	store.record("starting")
	return nil
}

func (store *attemptMemory) AttemptStopRequested(context.Context, catalogmodel.ID) (bool, error) {
	return store.stop.Load(), nil
}

func (store *attemptMemory) RecordingStarted(context.Context, catalogmodel.ID, time.Time) (time.Time, error) {
	store.record("recording")
	if store.startEnd.IsZero() {
		store.startEnd = store.end
	}
	return store.startEnd, nil
}

func (store *attemptMemory) OneSegRecordingStarted(context.Context, catalogmodel.ID, time.Time) error {
	store.record("one-seg-recording")
	return nil
}

func (store *attemptMemory) UpdateRecordingProgress(_ context.Context, _ catalogmodel.ID, count int64, _ time.Time) (time.Time, error) {
	store.mu.Lock()
	store.progress = append(store.progress, count)
	if store.progressEnd.IsZero() {
		store.progressEnd = store.end
	}
	end, err := store.progressEnd, store.progressErr
	store.mu.Unlock()
	store.record("progress")
	return end, err
}

func (store *attemptMemory) UpdateOneSegProgress(_ context.Context, _ catalogmodel.ID, count int64,
	_ time.Time,
) (time.Time, error) {
	store.mu.Lock()
	if store.progressEnd.IsZero() {
		store.progressEnd = store.end
	}
	end, err := store.progressEnd, store.progressErr
	store.mu.Unlock()
	store.record("one-seg-progress")
	return end, err
}

func (store *attemptMemory) BeginFinalization(_ context.Context, request core.FinalizeRequest) error {
	store.finalize = request
	store.record("finalizing")
	return store.finalizeErr
}

func (store *attemptMemory) MarkFinalPublished(context.Context, catalogmodel.ID, time.Time) error {
	store.record("published")
	return store.publishErr
}

func (store *attemptMemory) MarkDirectorySynced(context.Context, catalogmodel.ID, time.Time) error {
	store.record("directory-recorded")
	return store.directoryErr
}

func (store *attemptMemory) MarkOneSegFinalPublished(context.Context, catalogmodel.ID, time.Time) error {
	store.record("one-seg-published")
	return store.publishErr
}

func (store *attemptMemory) MarkOneSegDirectorySynced(context.Context, catalogmodel.ID, time.Time) error {
	store.record("one-seg-directory-recorded")
	return store.directoryErr
}

func (store *attemptMemory) SetOneSegOutcome(_ context.Context, _ catalogmodel.ID, outcome core.OneSegResult,
	_ time.Time,
) error {
	store.record("one-seg-outcome")
	store.finalize.OneSeg = &outcome
	return nil
}

func (store *attemptMemory) FinishAttempt(_ context.Context, request core.FinishRequest) error {
	store.finish = request
	store.record("finished")
	return store.finishErr
}

type fakePartial struct {
	operations *[]string
	short      bool
	syncErr    error
	closeErr   error
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
	return file.syncErr
}

func (file *fakePartial) Close() error {
	*file.operations = append(*file.operations, "file-close")
	return file.closeErr
}

type fakeProvider struct {
	lease      providerstream.Lease
	err        error
	leases     []providerstream.Lease
	errors     []error
	opens      int
	request    providerstream.Request
	requests   []providerstream.Request
	operations *[]string
}

func (stream *fakeProvider) OpenStream(_ context.Context, request providerstream.Request) (providerstream.Lease, error) {
	index := stream.opens
	stream.opens++
	stream.request = request
	stream.requests = append(stream.requests, request)
	if stream.operations != nil {
		*stream.operations = append(*stream.operations, "open")
	}
	if index < len(stream.errors) && stream.errors[index] != nil {
		return nil, stream.errors[index]
	}
	if index < len(stream.leases) && stream.leases[index] != nil {
		return stream.leases[index], nil
	}
	return stream.lease, stream.err
}

type fakeLease struct {
	read       func([]byte) (int, providerstream.Terminal, error)
	cancel     int
	close      int
	operations *[]string
}

func (lease *fakeLease) Read(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
	return lease.read(destination)
}

func (lease *fakeLease) Cancel() error {
	lease.cancel++
	if lease.operations != nil {
		*lease.operations = append(*lease.operations, "lease-cancel")
	}
	return nil
}

func (lease *fakeLease) Close() error {
	lease.close++
	if lease.operations != nil {
		*lease.operations = append(*lease.operations, "lease-close")
	}
	return nil
}

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

type lockedClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *lockedClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *lockedClock) set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type concurrentEvents struct {
	mu     sync.Mutex
	values []string
}

func (events *concurrentEvents) add(value string) {
	events.mu.Lock()
	events.values = append(events.values, value)
	events.mu.Unlock()
}

func (events *concurrentEvents) snapshot() []string {
	events.mu.Lock()
	defer events.mu.Unlock()
	return append([]string(nil), events.values...)
}

type coordinatedProvider struct {
	events    *concurrentEvents
	main      providerstream.Lease
	oneSeg    providerstream.Lease
	oneSegErr error
	mu        sync.Mutex
	requests  []providerstream.Request
}

func (stream *coordinatedProvider) OpenStream(_ context.Context,
	request providerstream.Request,
) (providerstream.Lease, error) {
	stream.mu.Lock()
	stream.requests = append(stream.requests, request)
	stream.mu.Unlock()
	stream.events.add("open-" + request.Target.Opaque)
	if request.Target.Opaque == "1003" {
		return stream.main, nil
	}
	if request.Target.Opaque == "1004" {
		return stream.oneSeg, stream.oneSegErr
	}
	return nil, provider.NewFailure(provider.ReasonNotFound, "test")
}

type coordinatedLease struct {
	read   func(context.Context, []byte) (int, providerstream.Terminal, error)
	cancel atomic.Int32
	close  atomic.Int32
}

func (lease *coordinatedLease) Read(ctx context.Context,
	destination []byte,
) (int, providerstream.Terminal, error) {
	return lease.read(ctx, destination)
}

func (lease *coordinatedLease) Cancel() error { lease.cancel.Add(1); return nil }
func (lease *coordinatedLease) Close() error  { lease.close.Add(1); return nil }

type concurrentPartial struct {
	events *concurrentEvents
	name   string
}

func (file *concurrentPartial) Write(data []byte) (int, error) {
	file.events.add("write-" + file.name)
	return len(data), nil
}

func (file *concurrentPartial) Sync() error  { file.events.add("sync-" + file.name); return nil }
func (file *concurrentPartial) Close() error { file.events.add("close-" + file.name); return nil }

func TestExecutorRecordsOneSegAfterMainStartsAndPublishesMainFirst(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	clock := &lockedClock{now: start}
	events := &concurrentEvents{}
	store := &attemptMemory{start: start, end: end, onOperation: events.add}
	oneSegWritten := make(chan struct{})
	releaseOneSeg := make(chan struct{})
	mainLease := &coordinatedLease{read: func(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
		<-oneSegWritten
		copy(destination, bytesOf(0x47, 188))
		clock.set(end)
		close(releaseOneSeg)
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	oneSegLease := &coordinatedLease{read: func(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, 188))
		close(oneSegWritten)
		<-releaseOneSeg
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &coordinatedProvider{events: events, main: mainLease, oneSeg: oneSegLease}
	ids := []catalogmodel.ID{appID(t, 40), appID(t, 41), appID(t, 42), appID(t, 44)}
	idIndex := 0
	executor := Executor{
		Store: store, Stream: stream, Clock: clock, OwnerID: appID(t, 43), Generation: 1,
		NewID: func() (catalogmodel.ID, error) {
			if idIndex >= len(ids) {
				return catalogmodel.ID{}, errors.New("too many ids")
			}
			id := ids[idIndex]
			idIndex++
			return id, nil
		},
		Files: FileOperations{
			CreatePartial: func(plan core.FilePlan) (PartialFile, error) {
				name := "main"
				if strings.Contains(plan.FinalPath, ".oneseg.") {
					name = "one-seg"
				}
				events.add("create-" + name)
				return &concurrentPartial{events: events, name: name}, nil
			},
			LinkFinal: func(plan core.FilePlan) error {
				events.add("link-" + plan.FinalPath)
				return nil
			},
			SyncDirectory: func(plan core.FilePlan) error {
				events.add("directory-" + plan.FinalPath)
				return nil
			},
			RemovePartial: func(plan core.FilePlan) error {
				events.add("remove-" + plan.FinalPath)
				return nil
			},
		},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		Wait: func(context.Context, time.Duration) error { return nil },
	}
	reservation := reservationForExecutor(t, start, time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result, err := executor.Execute(context.Background(), reservation)
	if err != nil || result.State != core.AttemptSucceeded || store.finalize.OneSeg == nil ||
		!store.finalize.OneSeg.Publish || store.finalize.OneSeg.ByteCount != 188 || store.claim.OneSegPlan == nil {
		t.Fatalf("result=%+v finalize=%+v claim=%+v err=%v", result, store.finalize, store.claim, err)
	}
	values := events.snapshot()
	for _, pair := range [][2]string{
		{"create-main", "open-1003"}, {"open-1003", "recording"}, {"recording", "create-one-seg"},
		{"create-one-seg", "open-1004"}, {"directory-recorded", "link-" + store.claim.OneSegPlan.FinalPath},
		{"one-seg-directory-recorded", "finished"},
	} {
		if eventIndex(values, pair[0]) < 0 || eventIndex(values, pair[0]) >= eventIndex(values, pair[1]) {
			t.Fatalf("order %q < %q events=%v", pair[0], pair[1], values)
		}
	}
	if mainLease.cancel.Load() != 1 || mainLease.close.Load() != 1 ||
		oneSegLease.cancel.Load() != 1 || oneSegLease.close.Load() != 1 {
		t.Fatalf("main=%d/%d one_seg=%d/%d", mainLease.cancel.Load(), mainLease.close.Load(),
			oneSegLease.cancel.Load(), oneSegLease.close.Load())
	}
}

func TestExecutorPublishesSyncedOneSegStoppedAtNormalMainEnd(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	clock := &lockedClock{now: start}
	events := &concurrentEvents{}
	store := &attemptMemory{start: start, end: end, onOperation: events.add}
	oneSegWritten := make(chan struct{})
	mainLease := &coordinatedLease{read: func(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
		<-oneSegWritten
		copy(destination, bytesOf(0x47, 188))
		clock.set(end)
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	var oneSegReads atomic.Int32
	oneSegLease := &coordinatedLease{read: func(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
		if oneSegReads.Add(1) == 1 {
			copy(destination, bytesOf(0x47, 188))
			close(oneSegWritten)
			return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
		}
		<-ctx.Done()
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled},
			provider.NewFailure(provider.ReasonCancelled, "test")
	}}
	stream := &coordinatedProvider{events: events, main: mainLease, oneSeg: oneSegLease}
	executor := oneSegExecutorForTest(t, store, stream, clock, events)
	reservation := reservationForExecutor(t, start, time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result, err := executor.Execute(context.Background(), reservation)
	if err != nil || result.State != core.AttemptSucceeded || store.finalize.OneSeg == nil ||
		!store.finalize.OneSeg.Publish || store.finalize.OneSeg.ByteCount != 188 ||
		store.finalize.OneSeg.Reason != core.ReasonCompleted {
		t.Fatalf("result=%+v oneSeg=%+v err=%v", result, store.finalize.OneSeg, err)
	}
}

func TestOneSegCoordinatorPublishesUsefulAuxiliaryThatObservedUserStopFirst(t *testing.T) {
	for _, reason := range []core.TerminalReason{core.ReasonUserRequestedStop, core.ReasonStreamCancelled} {
		t.Run(string(reason), func(t *testing.T) {
			done := make(chan core.OneSegResult, 1)
			done <- core.OneSegResult{
				ByteCount: minimumUsefulTS, Availability: core.AvailabilityPartial,
				Reason: reason, FileSynced: true,
			}
			coordinator := &oneSegCoordinator{
				done: done, started: true, cancel: func() {},
			}
			result := coordinator.join(core.ReasonUserRequestedStop, true)
			if !result.Publish || result.ByteCount != minimumUsefulTS ||
				result.Reason != core.ReasonUserRequestedStop || result.Availability != core.AvailabilityPartial {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestExecutorKeepsMainSuccessWhenOneSegOpenFails(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	clock := &lockedClock{now: start}
	events := &concurrentEvents{}
	store := &attemptMemory{start: start, end: end, onOperation: events.add}
	oneSegOpened := make(chan struct{})
	mainLease := &coordinatedLease{read: func(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
		<-oneSegOpened
		copy(destination, bytesOf(0x47, 188))
		clock.set(end)
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &coordinatedProvider{
		events: events, main: mainLease,
		oneSegErr: provider.NewFailure(provider.ReasonNotFound, "test"),
	}
	previous := stream.oneSegErr
	stream.oneSeg = &coordinatedLease{read: func(context.Context, []byte) (int, providerstream.Terminal, error) {
		return 0, providerstream.Terminal{}, previous
	}}
	originalOpen := stream.OpenStream
	wrappedStream := providerFunc(func(ctx context.Context, request providerstream.Request) (providerstream.Lease, error) {
		lease, err := originalOpen(ctx, request)
		if request.Target.Opaque == "1004" {
			close(oneSegOpened)
		}
		return lease, err
	})
	executor := oneSegExecutorForTest(t, store, wrappedStream, clock, events)
	reservation := reservationForExecutor(t, start, time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result, err := executor.Execute(context.Background(), reservation)
	if err != nil || result.State != core.AttemptSucceeded || store.finish.State != core.AttemptSucceeded ||
		store.finalize.OneSeg == nil || store.finalize.OneSeg.Publish ||
		store.finalize.OneSeg.Reason != core.ReasonStreamNotFound {
		t.Fatalf("result=%+v finalize=%+v finish=%+v err=%v", result, store.finalize, store.finish, err)
	}
	if eventIndex(events.snapshot(), "one-seg-published") >= 0 {
		t.Fatalf("ワンセグ失敗後に完成名を公開しました: %v", events.snapshot())
	}
}

func TestExecutorJoinsOneSegBeforeFailedMainBecomesTerminal(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &lockedClock{now: start}
	events := &concurrentEvents{}
	store := &attemptMemory{start: start, end: start.Add(time.Minute), onOperation: events.add}
	oneSegStarted := make(chan struct{})
	mainLease := &coordinatedLease{read: func(_ context.Context, _ []byte) (int, providerstream.Terminal, error) {
		<-oneSegStarted
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalRejected},
			provider.NewFailure(provider.ReasonRejected, "test")
	}}
	oneSegLease := &coordinatedLease{read: func(ctx context.Context, _ []byte) (int, providerstream.Terminal, error) {
		close(oneSegStarted)
		<-ctx.Done()
		events.add("one-seg-read-stopped")
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled},
			provider.NewFailure(provider.ReasonCancelled, "test")
	}}
	stream := &coordinatedProvider{events: events, main: mainLease, oneSeg: oneSegLease}
	executor := oneSegExecutorForTest(t, store, stream, clock, events)
	reservation := reservationForExecutor(t, start, time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	result, err := executor.Execute(context.Background(), reservation)
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonStreamUnavailable ||
		store.finish.OneSeg == nil {
		t.Fatalf("result=%+v finish=%+v err=%v", result, store.finish, err)
	}
	values := events.snapshot()
	if eventIndex(values, "one-seg-read-stopped") < 0 ||
		eventIndex(values, "one-seg-read-stopped") >= eventIndex(values, "finished") {
		t.Fatalf("補助処理の終了前にterminalへ進みました: %v", values)
	}
}

func TestOneSegReconnectsIndependentlyAndUsesDistinctCorrelationIDs(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: end, progressEnd: end}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, 188))
		clock.now = end
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &fakeProvider{
		errors: []error{provider.NewFailure(provider.ReasonTimeout, "test")},
		leases: []providerstream.Lease{nil, lease},
	}
	operations := []string{}
	plan := core.FilePlan{PartialPath: "2026/08/program.oneseg.ts.partial", FinalPath: "2026/08/program.oneseg.ts"}
	attempt := core.Attempt{ID: appID(t, 65), PlannedStart: start, PlannedEnd: end, OneSegPlan: &plan}
	reservation := reservationForExecutor(t, start, 2*time.Minute)
	reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	executor := Executor{
		Store: store, Stream: stream, Clock: clock,
		Files: FileOperations{CreatePartial: func(core.FilePlan) (PartialFile, error) {
			return &fakePartial{operations: &operations}, nil
		}},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		Wait: func(context.Context, time.Duration) error { return nil },
	}
	result := executor.runOneSeg(context.Background(), reservation, attempt, end, start.Add(core.MaxEffectiveDuration))
	if !result.Publish || result.ByteCount != 188 || result.Reason != core.ReasonCompletedAfterReconnect ||
		stream.opens != 2 || len(stream.requests) != 2 ||
		stream.requests[0].CorrelationID != attempt.ID.String()+"-oneseg" ||
		stream.requests[1].CorrelationID != attempt.ID.String()+"-oneseg-reconnect-1" ||
		lease.cancel != 1 || lease.close != 1 {
		t.Fatalf("result=%+v requests=%+v cancel=%d close=%d", result, stream.requests, lease.cancel, lease.close)
	}
}

func TestOneSegFileFailuresStaySeparateFromMainResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		createErr  error
		shortWrite bool
		syncErr    error
		closeErr   error
		want       core.TerminalReason
		missing    bool
	}{
		{name: "create", createErr: errors.New("test"), want: core.ReasonFileCreateFailed, missing: true},
		{name: "write", shortWrite: true, want: core.ReasonFileWriteFailed},
		{name: "sync", syncErr: errors.New("test"), want: core.ReasonFileSyncFailed},
		{name: "close", closeErr: errors.New("test"), want: core.ReasonFileSyncFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
			end := start.Add(time.Minute)
			clock := &mutableClock{now: start}
			store := &attemptMemory{start: start, end: end, progressEnd: end}
			operations := []string{}
			file := &fakePartial{operations: &operations, short: test.shortWrite, syncErr: test.syncErr, closeErr: test.closeErr}
			lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
				copy(destination, bytesOf(0x47, 188))
				if !test.shortWrite {
					clock.now = end
				}
				return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
			}}
			stream := &fakeProvider{lease: lease}
			plan := core.FilePlan{PartialPath: "2026/08/program.oneseg.ts.partial", FinalPath: "2026/08/program.oneseg.ts"}
			attempt := core.Attempt{ID: appID(t, 66), PlannedStart: start, PlannedEnd: end, OneSegPlan: &plan}
			reservation := reservationForExecutor(t, start, time.Minute)
			reservation.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
			executor := Executor{
				Store: store, Stream: stream, Clock: clock,
				Files: FileOperations{CreatePartial: func(core.FilePlan) (PartialFile, error) {
					if test.createErr != nil {
						return nil, test.createErr
					}
					return file, nil
				}},
				WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
					return context.WithCancel(ctx)
				},
				Wait: func(context.Context, time.Duration) error { return nil },
			}
			result := executor.runOneSeg(context.Background(), reservation, attempt, end,
				start.Add(core.MaxEffectiveDuration))
			if result.Publish || result.Reason != test.want ||
				(test.missing && result.Availability != core.AvailabilityMissing) {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestOneSegPublicationFailureIsSavedWithoutFailingMain(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	store := &attemptMemory{}
	plan := core.FilePlan{PartialPath: "2026/08/program.oneseg.ts.partial", FinalPath: "2026/08/program.oneseg.ts"}
	coordinator := &oneSegCoordinator{
		executor: Executor{
			Store: store, Clock: &mutableClock{now: now},
			Files: FileOperations{LinkFinal: func(core.FilePlan) error { return errors.New("test") }},
		},
		attempt: core.Attempt{ID: appID(t, 67), OneSegPlan: &plan}, joined: true,
		result: core.OneSegResult{
			ByteCount: 188, Availability: core.AvailabilityPartial,
			Reason: core.ReasonCompleted, FileSynced: true, Publish: true,
		},
	}
	if err := coordinator.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.finalize.OneSeg == nil || store.finalize.OneSeg.Publish ||
		store.finalize.OneSeg.Reason != core.ReasonFinalPublicationFailed ||
		store.finalize.OneSeg.Availability != core.AvailabilityPartial {
		t.Fatalf("outcome=%+v", store.finalize.OneSeg)
	}
}

type providerFunc func(context.Context, providerstream.Request) (providerstream.Lease, error)

func (open providerFunc) OpenStream(ctx context.Context,
	request providerstream.Request,
) (providerstream.Lease, error) {
	return open(ctx, request)
}

func oneSegExecutorForTest(t *testing.T, store *attemptMemory, stream providerstream.Provider,
	clock TimeSource, events *concurrentEvents,
) Executor {
	t.Helper()
	ids := []catalogmodel.ID{appID(t, 60), appID(t, 61), appID(t, 62), appID(t, 63)}
	index := 0
	return Executor{
		Store: store, Stream: stream, Clock: clock, OwnerID: appID(t, 64), Generation: 1,
		NewID: func() (catalogmodel.ID, error) {
			if index >= len(ids) {
				return catalogmodel.ID{}, errors.New("too many ids")
			}
			id := ids[index]
			index++
			return id, nil
		},
		Files: FileOperations{
			CreatePartial: func(plan core.FilePlan) (PartialFile, error) {
				name := "main"
				if strings.Contains(plan.FinalPath, ".oneseg.") {
					name = "one-seg"
				}
				events.add("create-" + name)
				return &concurrentPartial{events: events, name: name}, nil
			},
			LinkFinal: func(plan core.FilePlan) error {
				events.add("link-" + plan.FinalPath)
				return nil
			},
			SyncDirectory: func(plan core.FilePlan) error {
				events.add("directory-" + plan.FinalPath)
				return nil
			},
			RemovePartial: func(plan core.FilePlan) error {
				events.add("remove-" + plan.FinalPath)
				return nil
			},
		},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		Wait: func(context.Context, time.Duration) error { return nil },
	}
}

func eventIndex(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func TestExecutorFailsMalformedSelectedStreamWithoutReconnect(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute)}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, tsPacketBytes))
		clock.now = store.end
		return tsPacketBytes, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, stream, clock, false)
	reservation := reservationForExecutor(t, start, time.Minute)
	reservation.Components = core.ComponentDefault
	result, err := executor.Execute(context.Background(), reservation)
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonStreamFormatInvalid ||
		stream.opens != 1 || store.finish.ByteCount != 0 {
		t.Fatalf("result=%+v opens=%d finish=%+v err=%v", result, stream.opens, store.finish, err)
	}
}

func TestExecutorUsesExtendedPlannedEnd(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	initialEnd := start.Add(time.Minute)
	extendedEnd := start.Add(2 * time.Minute)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: initialEnd, progressEnd: extendedEnd}
	reads := 0
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		reads++
		for index := 0; index < 188; index++ {
			destination[index] = 0x47
		}
		switch reads {
		case 1:
			clock.now = start.Add(progressInterval)
		case 2:
			clock.now = initialEnd
		default:
			clock.now = extendedEnd
		}
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	executor.WithDeadline = func(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
		if !deadline.Equal(start.Add(core.MaxEffectiveDuration)) {
			t.Fatalf("deadline=%s", deadline)
		}
		return context.WithCancel(ctx)
	}
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptSucceeded || result.Reason != core.ReasonCompleted ||
		reads != 3 || store.finish.ByteCount != 3*188 {
		t.Fatalf("result=%+v reads=%d finish=%+v err=%v", result, reads, store.finish, err)
	}
}

func TestExecutorUsesExtensionObservedBeforeRecordingStart(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	initialEnd := start.Add(time.Minute)
	extendedEnd := start.Add(2 * time.Minute)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: initialEnd, startEnd: extendedEnd, progressEnd: extendedEnd}
	reads := 0
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		reads++
		for index := 0; index < 188; index++ {
			destination[index] = 0x47
		}
		if reads == 1 {
			clock.now = initialEnd
		} else {
			clock.now = extendedEnd
		}
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptSucceeded || reads != 2 || store.finish.ByteCount != 2*188 {
		t.Fatalf("result=%+v reads=%d finish=%+v err=%v", result, reads, store.finish, err)
	}
}

func TestExecutorStopsSafelyWhenPastEndIsObservedBeforeStreamRead(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start.Add(10 * time.Second)}
	store := &attemptMemory{start: start, end: start.Add(time.Minute), startEnd: start.Add(time.Millisecond)}
	lease := &fakeLease{read: func([]byte) (int, providerstream.Terminal, error) {
		t.Fatal("終了済みの録画でstreamを読みました")
		return 0, providerstream.Terminal{}, nil
	}}
	stream := &fakeProvider{lease: lease}
	executor := executorForTest(t, store, stream, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonStreamEndedEarly ||
		store.finish.ByteCount != 0 || stream.opens != 1 || lease.cancel != 1 || lease.close != 1 {
		t.Fatalf("result=%+v finish=%+v opens=%d cancel=%d close=%d err=%v",
			result, store.finish, stream.opens, lease.cancel, lease.close, err)
	}
	if countString(store.operations, "write") != 0 || countString(store.operations, "progress") != 0 {
		t.Fatalf("終了済みの録画へ書き込みました: %v", store.operations)
	}
}

func TestExecutorRejectsInvalidPlannedEndUpdate(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	for _, plannedEnd := range []time.Time{start, start.Add(core.MaxEffectiveDuration + time.Millisecond)} {
		t.Run(plannedEnd.Sub(start).String(), func(t *testing.T) {
			clock := &mutableClock{now: start}
			store := &attemptMemory{start: start, end: start.Add(time.Minute), progressEnd: plannedEnd}
			lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
				for index := 0; index < 188; index++ {
					destination[index] = 0x47
				}
				clock.now = start.Add(progressInterval)
				return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
			}}
			executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
			result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
			if err != nil || result.State != core.AttemptPartial || result.Reason != core.ReasonProcessInterrupted {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestExecutorFinishesNormallyAfterPlannedEndShortening(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute), progressEnd: start.Add(4 * time.Second)}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, 188))
		clock.now = start.Add(progressInterval)
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptSucceeded || result.Reason != core.ReasonCompleted ||
		store.finish.ByteCount != 188 {
		t.Fatalf("result=%+v finish=%+v err=%v", result, store.finish, err)
	}
}

func TestExecutorExtensionOnlyRejectsPlannedEndShortening(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute), progressEnd: start.Add(30 * time.Second)}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, 188))
		clock.now = start.Add(progressInterval)
		return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	executor.FollowExtensionOnly = true
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptPartial || result.Reason != core.ReasonProcessInterrupted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExecutorKeepsEarlyStreamAsPartialWhenLessThanReconnectWindowRemains(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Minute - time.Nanosecond)}
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

func TestExecutorRecordsOpenTimeoutWithoutRetryInsideReconnectWindow(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(59 * time.Second)}
	stream := &fakeProvider{err: provider.NewFailure(provider.ReasonTimeout, "test")}
	executor := executorForTest(t, store, stream, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonStreamTimeout || stream.opens != 1 ||
		store.finish.Availability != core.AvailabilityPartial {
		t.Fatalf("result=%+v opens=%d err=%v", result, stream.opens, err)
	}
}

func TestExecutorReconnectsUpToThreeTimesAndKeepsOneRecording(t *testing.T) {
	for additionalConnections := 1; additionalConnections <= len(reconnectDelays); additionalConnections++ {
		t.Run(fmt.Sprintf("additional-%d", additionalConnections), func(t *testing.T) {
			start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
			clock := &mutableClock{now: start}
			store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
			leases := make([]providerstream.Lease, additionalConnections+1)
			for index := 0; index < additionalConnections; index++ {
				leases[index] = &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
					copy(destination, bytesOf(0x47, minimumUsefulTS))
					return minimumUsefulTS, providerstream.Terminal{Done: true, Reason: providerstream.TerminalEarlyEOF},
						provider.NewFailure(provider.ReasonEarlyEOF, "test")
				}}
			}
			leases[additionalConnections] = &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
				copy(destination, bytesOf(0x47, minimumUsefulTS))
				clock.now = store.end
				return minimumUsefulTS, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
			}}
			stream := &fakeProvider{leases: leases}
			executor := executorForTest(t, store, stream, clock, false)
			var waits []time.Duration
			executor.Wait = func(_ context.Context, delay time.Duration) error {
				waits = append(waits, delay)
				return nil
			}

			result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
			if err != nil || result.State != core.AttemptSucceeded || result.Reason != core.ReasonCompletedAfterReconnect {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if stream.opens != additionalConnections+1 || store.finish.ByteCount != int64(additionalConnections+1)*minimumUsefulTS ||
				countString(store.operations, "create") != 1 || countString(store.operations, "recording") != 1 {
				t.Fatalf("opens=%d finish=%+v operations=%v", stream.opens, store.finish, store.operations)
			}
			if !equalDurations(waits, reconnectDelays[:additionalConnections]) {
				t.Fatalf("waits=%v", waits)
			}
			seen := make(map[string]bool)
			for index, request := range stream.requests {
				if request.CorrelationID == "" || seen[request.CorrelationID] {
					t.Fatalf("correlation[%d]=%q", index, request.CorrelationID)
				}
				seen[request.CorrelationID] = true
			}
			for index, item := range leases {
				lease := item.(*fakeLease)
				if lease.cancel != 1 || lease.close != 1 {
					t.Fatalf("lease[%d] cancel=%d close=%d", index, lease.cancel, lease.close)
				}
			}
		})
	}
}

func TestExecutorExhaustsReconnectLimit(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	failure := provider.NewFailure(provider.ReasonUnavailable, "test")
	stream := &fakeProvider{errors: []error{failure, failure, failure, failure}}
	executor := executorForTest(t, store, stream, clock, false)
	var waits []time.Duration
	executor.Wait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}

	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
	if err != nil || result.State != core.AttemptFailed || result.Reason != core.ReasonStreamReconnectExhausted ||
		stream.opens != 4 || countString(store.operations, "recording") != 0 ||
		!equalDurations(waits, reconnectDelays[:]) {
		t.Fatalf("result=%+v opens=%d waits=%v operations=%v err=%v", result, stream.opens, waits, store.operations, err)
	}
}

func TestExecutorReconnectsAfterZeroProgressAndClosesLeaseBeforeWaiting(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	var order []string
	first := &fakeLease{operations: &order, read: func([]byte) (int, providerstream.Terminal, error) {
		return 0, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	second := &fakeLease{operations: &order, read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, minimumUsefulTS))
		clock.now = store.end
		return minimumUsefulTS, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &fakeProvider{leases: []providerstream.Lease{first, second}, operations: &order}
	executor := executorForTest(t, store, stream, clock, false)
	executor.Wait = func(context.Context, time.Duration) error {
		order = append(order, "wait")
		return nil
	}

	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
	wantPrefix := []string{"open", "lease-cancel", "lease-close", "wait", "open"}
	if err != nil || result.Reason != core.ReasonCompletedAfterReconnect || len(order) < len(wantPrefix) ||
		!equalStrings(order[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("result=%+v order=%v err=%v", result, order, err)
	}
}

func TestExecutorAllowsReconnectAtExactlySixtySeconds(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(minimumReconnectRemaining)}
	lease := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, minimumUsefulTS))
		clock.now = store.end
		return minimumUsefulTS, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &fakeProvider{
		leases: []providerstream.Lease{nil, lease},
		errors: []error{provider.NewFailure(provider.ReasonTimeout, "test"), nil},
	}
	executor := executorForTest(t, store, stream, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, time.Minute))
	if err != nil || result.Reason != core.ReasonCompletedAfterReconnect || stream.opens != 2 {
		t.Fatalf("result=%+v opens=%d err=%v", result, stream.opens, err)
	}
}

func TestExecutorCancelsReconnectWaitWithParent(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	stream := &fakeProvider{err: provider.NewFailure(provider.ReasonUnavailable, "test")}
	executor := executorForTest(t, store, stream, clock, false)
	ctx, cancel := context.WithCancel(context.Background())
	executor.Wait = func(waitCtx context.Context, _ time.Duration) error {
		cancel()
		return waitCtx.Err()
	}

	result, err := executor.Execute(ctx, reservationForExecutor(t, start, 2*time.Minute))
	if err != nil || result.State != core.AttemptCancelled || result.Reason != core.ReasonProcessShutdown || stream.opens != 1 {
		t.Fatalf("result=%+v opens=%d err=%v", result, stream.opens, err)
	}
}

func TestUserStopDuringReconnectWaitDoesNotOpenAnotherStream(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
	stream := &fakeProvider{err: provider.NewFailure(provider.ReasonUnavailable, "test")}
	executor := executorForTest(t, store, stream, clock, false)
	ctx, cancel := context.WithCancel(context.Background())
	executor.Wait = func(waitCtx context.Context, _ time.Duration) error {
		store.stop.Store(true)
		cancel()
		return waitCtx.Err()
	}
	result, err := executor.Execute(ctx, reservationForExecutor(t, start, 2*time.Minute))
	if err != nil || result.State != core.AttemptCancelled || result.Reason != core.ReasonUserRequestedStop ||
		stream.opens != 1 || countString(store.operations, "finalizing") != 0 {
		t.Fatalf("result=%+v opens=%d operations=%v err=%v", result, stream.opens, store.operations, err)
	}
}

func TestExecutorDoesNotReconnectNonTransientFailures(t *testing.T) {
	tests := []struct {
		reason provider.Reason
		want   core.TerminalReason
	}{
		{reason: provider.ReasonNotFound, want: core.ReasonStreamNotFound},
		{reason: provider.ReasonRejected, want: core.ReasonStreamUnavailable},
		{reason: provider.ReasonMalformed, want: core.ReasonStreamUnavailable},
		{reason: provider.ReasonCancelled, want: core.ReasonStreamCancelled},
		{reason: provider.ReasonNoTuner, want: core.ReasonStreamUnavailable},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
			clock := &mutableClock{now: start}
			store := &attemptMemory{start: start, end: start.Add(2 * time.Minute)}
			stream := &fakeProvider{err: provider.NewFailure(test.reason, "test")}
			executor := executorForTest(t, store, stream, clock, false)
			result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
			if err != nil || result.Reason != test.want || stream.opens != 1 {
				t.Fatalf("result=%+v opens=%d err=%v", result, stream.opens, err)
			}
		})
	}
}

func TestRetryableStreamFailureUsesStableProviderAndTerminalReasons(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		terminal providerstream.Terminal
		want     bool
	}{
		{name: "unavailable", err: provider.NewFailure(provider.ReasonUnavailable, "test"), want: true},
		{name: "timeout", err: provider.NewFailure(provider.ReasonTimeout, "test"), want: true},
		{name: "early eof", err: provider.NewFailure(provider.ReasonEarlyEOF, "test"), want: true},
		{name: "clean end", terminal: providerstream.Terminal{Done: true, Reason: providerstream.TerminalCleanEnd}, want: true},
		{name: "peer", terminal: providerstream.Terminal{Done: true, Reason: providerstream.TerminalPeer}, want: true},
		{name: "rejected", err: provider.NewFailure(provider.ReasonRejected, "test")},
		{name: "not found", err: provider.NewFailure(provider.ReasonNotFound, "test")},
		{name: "malformed", err: provider.NewFailure(provider.ReasonMalformed, "test")},
		{name: "cancelled", terminal: providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := retryableStreamFailure(test.err, test.terminal); got != test.want {
				t.Fatalf("retryable=%v want=%v", got, test.want)
			}
		})
	}
}

func TestExecutorDoesNotReconnectProgressDatabaseFailure(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(2 * time.Minute), progressErr: errors.New("database failed")}
	first := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, minimumUsefulTS))
		return minimumUsefulTS, providerstream.Terminal{Done: true, Reason: providerstream.TerminalEarlyEOF},
			provider.NewFailure(provider.ReasonEarlyEOF, "test")
	}}
	second := &fakeLease{read: func(destination []byte) (int, providerstream.Terminal, error) {
		copy(destination, bytesOf(0x47, minimumUsefulTS))
		clock.now = clock.now.Add(progressInterval)
		return minimumUsefulTS, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}}
	stream := &fakeProvider{leases: []providerstream.Lease{first, second}}
	executor := executorForTest(t, store, stream, clock, false)
	result, err := executor.Execute(context.Background(), reservationForExecutor(t, start, 2*time.Minute))
	if err == nil || result != (Result{}) || stream.opens != 2 {
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

func TestExecutorPublishesUsefulUserStoppedRecordingAsPartial(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Hour)}
	lease := &userStopLease{started: make(chan struct{}), clock: clock, count: minimumUsefulTS}
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
		t.Fatal("停止対象のstream readが始まりませんでした")
	}
	store.stop.Store(true)
	cancel()
	select {
	case outcome := <-done:
		if outcome.err != nil || outcome.result.State != core.AttemptPartial ||
			outcome.result.Reason != core.ReasonUserRequestedStop || store.finish.Availability != core.AvailabilityFinal ||
			store.finalize.State != core.AttemptPartial || store.finalize.Reason != core.ReasonUserRequestedStop {
			t.Fatalf("result=%+v finalize=%+v finish=%+v err=%v", outcome.result, store.finalize, store.finish, outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("利用者停止後に録画処理が終了しませんでした")
	}
	for _, operation := range []string{"finalizing", "link", "published", "directory-recorded", "finished"} {
		if countString(store.operations, operation) != 1 {
			t.Fatalf("operation=%s operations=%v", operation, store.operations)
		}
	}
}

func TestExecutorDoesNotPublishTooShortUserStoppedRecording(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Hour)}
	lease := &userStopLease{started: make(chan struct{}), clock: clock, count: minimumUsefulTS - 1}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		result, _ := executor.Execute(ctx, reservationForExecutor(t, start, time.Hour))
		done <- result
	}()
	select {
	case <-lease.started:
	case <-time.After(time.Second):
		t.Fatal("停止対象のstream readが始まりませんでした")
	}
	store.stop.Store(true)
	cancel()
	select {
	case result := <-done:
		if result.State != core.AttemptCancelled || result.Reason != core.ReasonUserRequestedStop ||
			store.finish.ByteCount != minimumUsefulTS-1 || store.finish.Availability != core.AvailabilityPartial {
			t.Fatalf("result=%+v finish=%+v", result, store.finish)
		}
	case <-time.After(time.Second):
		t.Fatal("利用者停止後に録画処理が終了しませんでした")
	}
	if countString(store.operations, "link") != 0 || countString(store.operations, "finalizing") != 0 {
		t.Fatalf("短すぎる録画を公開しました: %v", store.operations)
	}
}

func TestExecutorPublishesMultiChunkUserStoppedRecording(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Hour)}
	const chunks = 32
	lease := &userStopLease{started: make(chan struct{}), clock: clock, count: provider.MaxStreamChunk, chunks: chunks}
	executor := executorForTest(t, store, &fakeProvider{lease: lease}, clock, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() {
		result, _ := executor.Execute(ctx, reservationForExecutor(t, start, time.Hour))
		done <- result
	}()
	select {
	case <-lease.started:
	case <-time.After(time.Second):
		t.Fatal("複数chunkの書込みが終わりませんでした")
	}
	store.stop.Store(true)
	cancel()
	select {
	case result := <-done:
		wantBytes := int64(chunks * provider.MaxStreamChunk)
		if result.State != core.AttemptPartial || result.Reason != core.ReasonUserRequestedStop ||
			store.finish.ByteCount != wantBytes || store.finish.Availability != core.AvailabilityFinal {
			t.Fatalf("result=%+v finish=%+v want_bytes=%d", result, store.finish, wantBytes)
		}
	case <-time.After(time.Second):
		t.Fatal("利用者停止後に録画処理が終了しませんでした")
	}
}

func TestExecutorStopsBeforeCreatingFileWhenRequestAlreadyExists(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	clock := &mutableClock{now: start}
	store := &attemptMemory{start: start, end: start.Add(time.Hour)}
	store.stop.Store(true)
	stream := &fakeProvider{}
	result, err := executorForTest(t, store, stream, clock, false).Execute(
		context.Background(), reservationForExecutor(t, start, time.Hour))
	if err != nil || result.State != core.AttemptCancelled || result.Reason != core.ReasonUserRequestedStop ||
		store.finish.Availability != core.AvailabilityMissing || stream.opens != 0 || countString(store.operations, "create") != 0 {
		t.Fatalf("result=%+v finish=%+v opens=%d operations=%v err=%v",
			result, store.finish, stream.opens, store.operations, err)
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

func TestUserStoppedPublicationFailsAtEachDurabilityBoundary(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	attempt := core.Attempt{ID: appID(t, 80), Plan: core.FilePlan{
		PartialPath: "2026/08/stopped.ts.partial", FinalPath: "2026/08/stopped.ts",
	}}
	tests := []struct {
		name       string
		fail       string
		wantReason core.TerminalReason
	}{
		{name: "finalization database", fail: "finalize"},
		{name: "final name conflict", fail: "conflict", wantReason: core.ReasonFinalNameConflict},
		{name: "link", fail: "link", wantReason: core.ReasonFinalPublicationFailed},
		{name: "publication database", fail: "published", wantReason: core.ReasonFinalDatabaseFailed},
		{name: "publication directory sync", fail: "sync-1", wantReason: core.ReasonFileSyncFailed},
		{name: "partial name removal", fail: "remove", wantReason: core.ReasonFinalPublicationFailed},
		{name: "removal directory sync", fail: "sync-2", wantReason: core.ReasonFileSyncFailed},
		{name: "directory database", fail: "directory", wantReason: core.ReasonFinalDatabaseFailed},
		{name: "terminal database", fail: "finish", wantReason: core.ReasonFinalDatabaseFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := errors.New("injected failure")
			store := &attemptMemory{start: start, end: start.Add(time.Hour)}
			switch test.fail {
			case "finalize":
				store.finalizeErr = failure
			case "published":
				store.publishErr = failure
			case "directory":
				store.directoryErr = failure
			case "finish":
				store.finishErr = failure
			}
			syncCalls := 0
			executor := Executor{
				Store: store, Clock: &mutableClock{now: start}, NewID: func() (catalogmodel.ID, error) { return appID(t, 81), nil },
				Files: FileOperations{
					CreatePartial: func(core.FilePlan) (PartialFile, error) { return nil, failure },
					LinkFinal: func(core.FilePlan) error {
						if test.fail == "conflict" {
							return core.ErrFinalExists
						}
						if test.fail == "link" {
							return failure
						}
						return nil
					},
					SyncDirectory: func(core.FilePlan) error {
						syncCalls++
						if test.fail == "sync-1" && syncCalls == 1 || test.fail == "sync-2" && syncCalls == 2 {
							return failure
						}
						return nil
					},
					RemovePartial: func(core.FilePlan) error {
						if test.fail == "remove" {
							return failure
						}
						return nil
					},
				},
			}
			result, err := executor.publishFinal(context.Background(), attempt, 188,
				core.AttemptPartial, core.ReasonUserRequestedStop)
			if err == nil {
				t.Fatal("失敗を成功として返しました")
			}
			if test.wantReason == "" {
				if result != (Result{}) {
					t.Fatalf("result=%+v", result)
				}
			} else if result.State != core.AttemptFinalizing || result.Reason != test.wantReason {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestUserStopDoesNotPublishWhenFileSyncOrCloseFails(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	for _, failureAt := range []string{"sync", "close"} {
		t.Run(failureAt, func(t *testing.T) {
			store := &attemptMemory{start: start, end: start.Add(time.Hour)}
			file := &fakePartial{operations: &store.operations}
			if failureAt == "sync" {
				file.syncErr = errors.New("sync failed")
			} else {
				file.closeErr = errors.New("close failed")
			}
			executor := Executor{Store: store, Clock: &mutableClock{now: start}}
			result, err := executor.finishUserStop(context.Background(), file,
				core.Reservation{}, core.Attempt{ID: appID(t, 82)}, minimumUsefulTS)
			if err != nil || result.State != core.AttemptPartial || result.Reason != core.ReasonFileSyncFailed ||
				store.finish.Availability != core.AvailabilityPartial || countString(store.operations, "finalizing") != 0 {
				t.Fatalf("result=%+v finish=%+v operations=%v err=%v", result, store.finish, store.operations, err)
			}
		})
	}
}

func TestPostRecordingRunsOnlyAfterSuccessfulFinalization(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	store := &attemptMemory{start: start, end: start.Add(time.Hour)}
	attempt := core.Attempt{ID: appID(t, 83), Plan: core.FilePlan{PartialPath: "2026/08/test.part", FinalPath: "2026/08/test.ts"}}
	reservation := core.Reservation{Number: 17, PostRecording: core.PostRecordingSettings{
		Mode: core.PostRecordingStandby, Script: "/allowed/finish.sh",
	}}
	called := 0
	observed := ""
	executor := Executor{
		Store: store, Clock: &mutableClock{now: start}, NewID: func() (catalogmodel.ID, error) { return appID(t, 84), nil },
		Files: FileOperations{
			LinkFinal: func(core.FilePlan) error { return nil }, SyncDirectory: func(core.FilePlan) error { return nil },
			RemovePartial: func(core.FilePlan) error { return nil },
			FinalPath:     func(core.FilePlan) (string, error) { return "/recordings/2026/08/test.ts", nil },
		},
		PostRecording: func(_ context.Context, request PostRecordingRequest) string {
			called++
			if store.finish.State != core.AttemptSucceeded || store.finish.Availability != core.AvailabilityFinal ||
				request.Script != reservation.PostRecording.Script || request.RecordingNumber != reservation.Number ||
				request.FinalPath != "/recordings/2026/08/test.ts" || request.State != core.AttemptSucceeded ||
				request.Reason != core.ReasonCompleted {
				t.Fatalf("finish=%+v request=%+v", store.finish, request)
			}
			return "post-recording-script-exit-failed"
		},
		ObservePostRecording: func(reason string) { observed = reason },
	}
	result, err := executor.publishAndPostProcess(context.Background(), reservation, attempt, 188,
		core.AttemptSucceeded, core.ReasonCompleted)
	if err != nil || result.State != core.AttemptSucceeded || result.PostRecording != core.PostRecordingStandby ||
		called != 1 || observed != "post-recording-script-exit-failed" {
		t.Fatalf("result=%+v called=%d observed=%q err=%v", result, called, observed, err)
	}
}

func TestPostRecordingIsSkippedWithoutScriptOrWhenPublicationFails(t *testing.T) {
	for _, test := range []struct {
		name, script string
		linkErr      error
	}{
		{name: "no script"},
		{name: "publication failure", script: "/allowed/finish.sh", linkErr: errors.New("link failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
			store := &attemptMemory{start: start, end: start.Add(time.Hour)}
			called := 0
			executor := Executor{
				Store: store, Clock: &mutableClock{now: start}, NewID: func() (catalogmodel.ID, error) { return appID(t, 85), nil },
				Files: FileOperations{LinkFinal: func(core.FilePlan) error { return test.linkErr },
					SyncDirectory: func(core.FilePlan) error { return nil }, RemovePartial: func(core.FilePlan) error { return nil },
					FinalPath: func(core.FilePlan) (string, error) { return "/recordings/test.ts", nil }},
				PostRecording: func(context.Context, PostRecordingRequest) string { called++; return "" },
			}
			result, resultErr := executor.publishAndPostProcess(context.Background(), core.Reservation{
				Number: 1, PostRecording: core.PostRecordingSettings{Mode: core.PostRecordingShutdown, Script: test.script},
			}, core.Attempt{ID: appID(t, 86)}, 188, core.AttemptSucceeded, core.ReasonCompleted)
			if called != 0 {
				t.Fatalf("post recording calls=%d", called)
			}
			if test.linkErr == nil && (resultErr != nil || result.PostRecording != core.PostRecordingShutdown) {
				t.Fatalf("result=%+v err=%v", result, resultErr)
			}
			if test.linkErr != nil && result.PostRecording.ChangesPower() {
				t.Fatalf("publication failure returned power candidate: %+v", result)
			}
		})
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

type userStopLease struct {
	started chan struct{}
	clock   *mutableClock
	reads   int
	count   int
	chunks  int
}

func (lease *userStopLease) Read(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
	lease.reads++
	chunks := lease.chunks
	if chunks == 0 {
		chunks = 1
	}
	if lease.reads <= chunks {
		copy(destination, bytesOf(0x47, lease.count))
		lease.clock.now = lease.clock.now.Add(2 * time.Second)
		return lease.count, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
	}
	close(lease.started)
	<-ctx.Done()
	return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled},
		provider.NewFailure(provider.ReasonCancelled, "test")
}

func (*userStopLease) Cancel() error { return nil }
func (*userStopLease) Close() error  { return nil }

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
		Wait: func(context.Context, time.Duration) error { return nil },
	}
}

func reservationForExecutor(t *testing.T, start time.Time, duration time.Duration) core.Reservation {
	t.Helper()
	return core.Reservation{
		ID: appID(t, 30), Version: 1, State: core.ReservationActive, Priority: 3, Components: core.ComponentBoth,
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

func equalDurations(left, right []time.Duration) bool {
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

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}
