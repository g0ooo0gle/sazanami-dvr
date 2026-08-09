//go:build unix

package sqlite

import (
	"bytes"
	"context"
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
	stream := &e2eStream{clock: clock, end: reservation.Program.Start.Add(40 * time.Minute)}
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
	stream := &e2eStream{clock: clock, end: reservation.Program.Start.Add(reservation.Program.Duration), disconnectFirst: true}
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
	if next, err := store.NextActiveReservation(context.Background()); err != nil || next != nil {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}
