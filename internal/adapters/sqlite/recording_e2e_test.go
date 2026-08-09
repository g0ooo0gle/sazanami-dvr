//go:build unix

package sqlite

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/recordingfs"
	apprecording "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

type e2eClock struct{ now time.Time }

func (clock *e2eClock) Now() time.Time { return clock.now }

type e2eStream struct {
	clock           *e2eClock
	end             time.Time
	opens           int
	disconnectFirst bool
	onRead          func()
	hooked          bool
}

func (stream *e2eStream) OpenStream(context.Context, providerstream.Request) (providerstream.Lease, error) {
	connection := stream.opens
	stream.opens++
	return &e2eLease{stream: stream, connection: connection}, nil
}

type e2eLease struct {
	stream     *e2eStream
	connection int
}

type stopE2EStream struct {
	clock  *e2eClock
	stop   func() error
	called bool
}

func (stream *stopE2EStream) OpenStream(context.Context, providerstream.Request) (providerstream.Lease, error) {
	return &stopE2ELease{stream: stream}, nil
}

type stopE2ELease struct{ stream *stopE2EStream }

func (lease *stopE2ELease) Read(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
	copy(destination, bytes.Repeat([]byte{0x47}, 188))
	if !lease.stream.called {
		lease.stream.called = true
		if err := lease.stream.stop(); err != nil {
			return 0, providerstream.Terminal{}, err
		}
	}
	lease.stream.clock.now = lease.stream.clock.now.Add(5 * time.Second)
	return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func (*stopE2ELease) Cancel() error { return nil }
func (*stopE2ELease) Close() error  { return nil }

type parallelE2EStream struct {
	clock   *e2eClock
	end     time.Time
	started chan<- struct{}
	release <-chan struct{}
}

func (stream *parallelE2EStream) OpenStream(context.Context, providerstream.Request) (providerstream.Lease, error) {
	return &parallelE2ELease{stream: stream}, nil
}

type parallelE2ELease struct{ stream *parallelE2EStream }

func (lease *parallelE2ELease) Read(ctx context.Context, destination []byte) (int, providerstream.Terminal, error) {
	select {
	case lease.stream.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return 0, providerstream.Terminal{Done: true, Reason: providerstream.TerminalCancelled}, ctx.Err()
	case <-lease.stream.release:
	}
	copy(destination, bytes.Repeat([]byte{0x47}, 188))
	lease.stream.clock.now = lease.stream.end
	return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func (*parallelE2ELease) Cancel() error { return nil }
func (*parallelE2ELease) Close() error  { return nil }

func (lease *e2eLease) Read(_ context.Context, destination []byte) (int, providerstream.Terminal, error) {
	copy(destination, bytes.Repeat([]byte{0x47}, 188))
	if lease.stream.onRead != nil && !lease.stream.hooked {
		lease.stream.hooked = true
		lease.stream.onRead()
	}
	if lease.stream.disconnectFirst && lease.connection == 0 {
		return 188, providerstream.Terminal{Done: true, Reason: providerstream.TerminalEarlyEOF},
			provider.NewFailure(provider.ReasonEarlyEOF, "test")
	}
	lease.stream.clock.now = lease.stream.end
	return 188, providerstream.Terminal{Reason: providerstream.TerminalActive}, nil
}

func TestActiveExtensionReachesRunningExecutor(t *testing.T) {
	_, store := openMigratedStore(t)
	defer store.Close()
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	target := storeFollowRevision(t, store, reservation, reservation.Program.Start, 40*time.Minute, 230)
	recordingRoot, err := recordingfs.OpenRoot(filepath.Join(t.TempDir(), "recordings"))
	if err != nil {
		t.Fatal(err)
	}
	defer recordingRoot.Close()
	clock := &e2eClock{now: reservation.Program.Start}
	var applied bool
	var followErr error
	stream := &e2eStream{clock: clock, end: reservation.Program.Start.Add(40*time.Minute + core.DefaultEndMargin)}
	stream.onRead = func() {
		applied, followErr = store.ApplyReservationFollow(context.Background(), core.ReservationFollowRequest{
			ReservationID: created.ID, ExpectedVersion: created.Version,
			ExpectedRevisionID: created.Program.ProgramRevisionID, TargetRevisionID: target.ProgramRevisionID,
			Now: reservation.Program.Start.Add(10 * time.Second),
		})
	}
	ids := []catalogmodel.ID{testID(t, 231), testID(t, 232), testID(t, 233)}
	nextID := 0
	executor := apprecording.Executor{
		Store: store, Stream: stream, Clock: clock, OwnerID: testID(t, 234), Generation: 1,
		NewID: func() (catalogmodel.ID, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
		Files: apprecording.FileOperations{
			CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) {
				return recordingRoot.CreatePartial(plan)
			},
			LinkFinal: recordingRoot.LinkFinal, SyncDirectory: recordingRoot.SyncDirectory,
			RemovePartial: recordingRoot.RemovePartial,
		},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
	}
	result, err := executor.Execute(context.Background(), created)
	if err != nil || followErr != nil || !applied || result.State != core.AttemptSucceeded ||
		result.Reason != core.ReasonCompleted || !stream.hooked {
		t.Fatalf("result=%+v applied=%v hooked=%v err=%v follow_err=%v",
			result, applied, stream.hooked, err, followErr)
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("active=%+v err=%v", items, err)
	}
	var plannedEndMS int64
	if err := store.reader.QueryRow(`SELECT planned_end_utc_ms FROM recording_attempts WHERE reservation_id=?`,
		created.ID.Bytes()).Scan(&plannedEndMS); err != nil || plannedEndMS != stream.end.UnixMilli() {
		t.Fatalf("planned_end=%d err=%v", plannedEndMS, err)
	}
}

func TestPersistedUserStopPublishesPartialFileWithoutInMemoryNotification(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	recordingRoot, err := recordingfs.OpenRoot(filepath.Join(t.TempDir(), "recordings"))
	if err != nil {
		t.Fatal(err)
	}
	defer recordingRoot.Close()
	clock := &e2eClock{now: reservation.Program.Start}
	stream := &stopE2EStream{clock: clock}
	stream.stop = func() error {
		result, stopErr := store.StopReservation(context.Background(), created.Number, clock.now.Add(time.Second))
		if stopErr != nil || !result.Notify || result.ReservationID != created.ID {
			return errors.New("unexpected stop result")
		}
		return nil
	}
	ids := []catalogmodel.ID{testID(t, 235), testID(t, 236), testID(t, 237)}
	nextID := 0
	executor := apprecording.Executor{
		Store: store, Stream: stream, Clock: clock, OwnerID: testID(t, 238), Generation: 1,
		NewID: func() (catalogmodel.ID, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
		Files: apprecording.FileOperations{
			CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) {
				return recordingRoot.CreatePartial(plan)
			},
			LinkFinal: recordingRoot.LinkFinal, SyncDirectory: recordingRoot.SyncDirectory,
			RemovePartial: recordingRoot.RemovePartial,
		},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
	}
	result, err := executor.Execute(context.Background(), created)
	if err != nil || result.State != core.AttemptPartial || result.Reason != core.ReasonUserRequestedStop || !stream.called {
		t.Fatalf("result=%+v called=%v err=%v", result, stream.called, err)
	}
	history, err := store.RecordingHistoryItem(context.Background(), created.Number)
	if err != nil || history == nil || !history.Playable() || history.ByteCount != 188 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	observation, err := recordingRoot.Inspect(history.Plan)
	if err != nil || observation.Partial.Exists || !observation.Final.Regular || observation.Final.Size != 188 {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
}

func (*e2eLease) Cancel() error { return nil }
func (*e2eLease) Close() error  { return nil }

func TestReservationToFinalFileSurvivesRestart(t *testing.T) {
	dataRoot, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	recordingRootPath := filepath.Join(t.TempDir(), "recordings")
	recordingRoot, err := recordingfs.OpenRoot(recordingRootPath)
	if err != nil {
		t.Fatal(err)
	}
	clock := &e2eClock{now: reservation.Program.Start}
	stream := &e2eStream{clock: clock, end: created.PlannedEnd(), disconnectFirst: true}
	ids := []catalogmodel.ID{testID(t, 130), testID(t, 131), testID(t, 132)}
	nextID := 0
	files := apprecording.FileOperations{
		CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) {
			return recordingRoot.CreatePartial(plan)
		},
		LinkFinal: recordingRoot.LinkFinal, SyncDirectory: recordingRoot.SyncDirectory, RemovePartial: recordingRoot.RemovePartial,
	}
	executor := apprecording.Executor{
		Store: store, Stream: stream, Files: files, Clock: clock, OwnerID: testID(t, 133), Generation: 1,
		NewID: func() (catalogmodel.ID, error) {
			id := ids[nextID]
			nextID++
			return id, nil
		},
		WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		Wait: func(context.Context, time.Duration) error { return nil },
	}
	result, err := executor.Execute(context.Background(), created)
	if err != nil || result.State != core.AttemptSucceeded || result.Reason != core.ReasonCompletedAfterReconnect || stream.opens != 2 {
		t.Fatalf("result=%+v opens=%d err=%v", result, stream.opens, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recordingRoot.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(context.Background(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recordingRoot, err = recordingfs.OpenRoot(recordingRootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer recordingRoot.Close()
	recovery := apprecording.Recovery{
		Store: store, Clock: clock,
		Files: apprecording.RecoveryFiles{
			FileOperations: apprecording.FileOperations{
				CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) {
					return recordingRoot.CreatePartial(plan)
				},
				LinkFinal: recordingRoot.LinkFinal, SyncDirectory: recordingRoot.SyncDirectory, RemovePartial: recordingRoot.RemovePartial,
			},
			Inspect: recordingRoot.Inspect,
		},
	}
	if err := recovery.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := store.RecoveryAttempts(context.Background(), core.MaxRecoveryPage, catalogmodel.ID{})
	if err != nil || len(items) != 1 || items[0].State != core.AttemptSucceeded || items[0].ByteCount != 376 ||
		items[0].Availability != core.AvailabilityFinal || items[0].Recovered || stream.opens != 2 {
		t.Fatalf("items=%+v opens=%d err=%v", items, stream.opens, err)
	}
	var terminalReason string
	if err := store.reader.QueryRow(`SELECT terminal_reason FROM recording_attempts WHERE id=?`, items[0].ID.Bytes()).Scan(&terminalReason); err != nil ||
		terminalReason != string(core.ReasonCompletedAfterReconnect) {
		t.Fatalf("terminal_reason=%q err=%v", terminalReason, err)
	}
	observation, err := recordingRoot.Inspect(items[0].Plan)
	if err != nil || observation.Partial.Exists || !observation.Final.Regular || observation.Final.Size != 376 {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
	if next, err := store.NextActiveReservation(context.Background(), time.Now().UTC()); err != nil || next != nil {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestTwoRecordingsUseSeparateDatabaseRowsAndFiles(t *testing.T) {
	dataRoot, store := openMigratedStore(t)
	firstRequest := reservationForTest(t, store)
	first, err := store.CreateReservation(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := secondReservationForE2E(t, store, firstRequest)
	second, err := store.CreateReservation(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	recordingRootPath := filepath.Join(t.TempDir(), "recordings")
	recordingRoot, err := recordingfs.OpenRoot(recordingRootPath)
	if err != nil {
		t.Fatal(err)
	}
	files := apprecording.FileOperations{
		CreatePartial: func(plan core.FilePlan) (apprecording.PartialFile, error) {
			return recordingRoot.CreatePartial(plan)
		},
		LinkFinal: recordingRoot.LinkFinal, SyncDirectory: recordingRoot.SyncDirectory,
		RemovePartial: recordingRoot.RemovePartial,
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	type execution struct {
		reservation core.Reservation
		clock       *e2eClock
		ids         []catalogmodel.ID
		owner       catalogmodel.ID
	}
	executions := []execution{
		{reservation: first, clock: &e2eClock{now: first.Program.Start},
			ids: []catalogmodel.ID{testID(t, 240), testID(t, 241), testID(t, 242)}, owner: testID(t, 243)},
		{reservation: second, clock: &e2eClock{now: second.Program.Start},
			ids: []catalogmodel.ID{testID(t, 244), testID(t, 245), testID(t, 246)}, owner: testID(t, 243)},
	}
	results := make(chan error, len(executions))
	for index := range executions {
		item := &executions[index]
		go func() {
			nextID := 0
			stream := &parallelE2EStream{
				clock: item.clock, end: item.reservation.PlannedEnd(),
				started: started, release: release,
			}
			executor := apprecording.Executor{
				Store: store, Stream: stream, Files: files, Clock: item.clock, OwnerID: item.owner, Generation: 1,
				NewID: func() (catalogmodel.ID, error) {
					id := item.ids[nextID]
					nextID++
					return id, nil
				},
				WithDeadline: func(ctx context.Context, _ time.Time) (context.Context, context.CancelFunc) {
					return context.WithCancel(ctx)
				},
			}
			result, executeErr := executor.Execute(context.Background(), item.reservation)
			if executeErr == nil && (result.State != core.AttemptSucceeded || result.Reason != core.ReasonCompleted) {
				executeErr = errors.New("unexpected recording result")
			}
			results <- executeErr
		}()
	}
	for range executions {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("二件の録画が同時にstream読込みへ到達しませんでした")
		}
	}
	close(release)
	for range executions {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.RecoveryAttempts(context.Background(), core.MaxRecoveryPage, catalogmodel.ID{})
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].Plan.PartialPath == items[1].Plan.PartialPath || items[0].Plan.FinalPath == items[1].Plan.FinalPath {
		t.Fatalf("録画ファイル名が共有されました: %+v %+v", items[0].Plan, items[1].Plan)
	}
	for _, item := range items {
		observation, inspectErr := recordingRoot.Inspect(item.Plan)
		if inspectErr != nil || item.State != core.AttemptSucceeded || item.ByteCount != 188 ||
			item.Availability != core.AvailabilityFinal || observation.Partial.Exists ||
			!observation.Final.Regular || observation.Final.Size != 188 {
			t.Fatalf("item=%+v observation=%+v err=%v", item, observation, inspectErr)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recordingRoot.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(context.Background(), dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	items, err = store.RecoveryAttempts(context.Background(), core.MaxRecoveryPage, catalogmodel.ID{})
	if err != nil || len(items) != 2 || items[0].State != core.AttemptSucceeded || items[1].State != core.AttemptSucceeded {
		t.Fatalf("restart items=%+v err=%v", items, err)
	}
}

func secondReservationForE2E(t *testing.T, store *Store, first core.Reservation) core.Reservation {
	t.Helper()
	syncID := testID(t, 237)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: first.Program.BackendID, StartedAtMS: 3, CorrelationID: "parallel-recording-test",
	}); err != nil {
		t.Fatal(err)
	}
	networkID, transportID, serviceID := int64(1), int64(2), int64(3)
	tuningTarget := first.Program.TuningTarget
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: tuningTarget, NetworkID: &networkID, TransportID: &transportID, ServiceID: &serviceID,
		DisplayName: "テスト局", TuningTarget: &tuningTarget, Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	startMS, durationMS, eventID := first.Program.Start.UnixMilli(), first.Program.Duration.Milliseconds(), int64(5)
	title := "並行録画テスト"
	if err := store.StorePrograms(context.Background(), syncID, false, []catalogmodel.ProgramObservation{{
		ServiceLocator: tuningTarget, EventLocator: "5", RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{
			StartUTCMS: &startMS, DurationMS: &durationMS, Title: &title, Validation: catalogmodel.ValidationValid,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, startMS+1, 1, 1); err != nil {
		t.Fatal(err)
	}
	programs, err := store.CurrentPrograms(context.Background(), first.Program.BackendID, 1, catalogmodel.ID{})
	if err != nil || len(programs) != 1 {
		t.Fatalf("programs=%+v err=%v", programs, err)
	}
	second := first
	second.ID = testID(t, 238)
	second.Program.ProgramInstanceID = programs[0].InstanceID
	second.Program.ProgramRevisionID = programs[0].RevisionID
	second.Program.EventID = uint16(eventID)
	second.Program.Title = title
	return second
}
