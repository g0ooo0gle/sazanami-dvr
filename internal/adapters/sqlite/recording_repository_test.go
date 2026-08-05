package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestReservationCreateReadbackAndDuplicate(t *testing.T) {
	root, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil || created.Number != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].ID != reservation.ID || items[0].Number != created.Number ||
		!items[0].RequestedFollow || items[0].EffectiveFollow || items[0].Program.Title != reservation.Program.Title {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	duplicate := reservation
	duplicate.ID = testID(t, 117)
	if _, err := store.CreateReservation(context.Background(), duplicate); !errors.Is(err, ErrDuplicateReservation) {
		t.Fatalf("duplicate err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err = reopened.ActiveReservations(context.Background(), 256, 0)
	if err != nil || len(items) != 1 || items[0].Number != 1 {
		t.Fatalf("restarted items=%+v err=%v", items, err)
	}
}

func TestActiveReservationsRejectsUnboundedQuery(t *testing.T) {
	_, store := openMigratedStore(t)
	for _, limit := range []int{0, recording.MaxPage + 1} {
		if _, err := store.ActiveReservations(context.Background(), limit, 0); err == nil {
			t.Fatalf("limit=%d が受理されました", limit)
		}
	}
}

func TestConcurrentReservationAddKeepsOneActiveReservation(t *testing.T) {
	_, store := openMigratedStore(t)
	first := reservationForTest(t, store)
	second := first
	second.ID = testID(t, 146)
	errorsFound := make(chan error, 2)
	for _, reservation := range []recording.Reservation{first, second} {
		reservation := reservation
		go func() {
			_, err := store.CreateReservation(context.Background(), reservation)
			errorsFound <- err
		}()
	}
	succeeded, duplicate := 0, 0
	for range 2 {
		switch err := <-errorsFound; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDuplicateReservation):
			duplicate++
		default:
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if succeeded != 1 || duplicate != 1 {
		t.Fatalf("succeeded=%d duplicate=%d", succeeded, duplicate)
	}
	items, err := store.ActiveReservations(context.Background(), recording.MaxPage, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("reservations=%d err=%v", len(items), err)
	}
}

func TestCurrentProgramsByServiceAndMatching(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	programs, err := store.CurrentProgramsByService(context.Background(), reservation.Program.BackendID, 1, catalogmodel.ProgramCursor{})
	if err != nil || len(programs) != 1 || programs[0].RawEventID == nil || *programs[0].RawEventID != int64(reservation.Program.EventID) {
		t.Fatalf("programs=%+v err=%v", programs, err)
	}
	cursor := catalogmodel.ProgramCursor{ServiceLocator: programs[0].ServiceLocator, EventLocator: programs[0].EventLocator}
	next, err := store.CurrentProgramsByService(context.Background(), reservation.Program.BackendID, 1, cursor)
	if err != nil || len(next) != 0 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	selected, err := store.CurrentProgramsForService(context.Background(), reservation.Program.BackendID,
		reservation.Program.ProviderServiceLocator, 1, "")
	if err != nil || len(selected) != 1 || selected[0].EventLocator != programs[0].EventLocator {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	selected, err = store.CurrentProgramsForService(context.Background(), reservation.Program.BackendID,
		reservation.Program.ProviderServiceLocator, 1, selected[0].EventLocator)
	if err != nil || len(selected) != 0 {
		t.Fatalf("selected next=%+v err=%v", selected, err)
	}
	matches, err := store.CurrentProgramsMatching(context.Background(), reservation.Program.BackendID,
		reservation.Program.ProviderServiceLocator, int64(reservation.Program.EventID), reservation.Program.Start.UnixMilli(), reservation.Program.Duration.Milliseconds())
	if err != nil || len(matches) != 1 || matches[0].InstanceID != reservation.Program.ProgramInstanceID {
		t.Fatalf("matches=%+v err=%v", matches, err)
	}
	if _, err := store.CurrentProgramsByService(context.Background(), reservation.Program.BackendID, 1,
		catalogmodel.ProgramCursor{ServiceLocator: "1003"}); err == nil {
		t.Fatal("不完全なcursorが受理されました")
	}
	if _, err := store.CurrentProgramsForService(context.Background(), reservation.Program.BackendID, "", 1, ""); err == nil {
		t.Fatal("空のservice locatorが受理されました")
	}
}

func TestRecordingAttemptLifecycle(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.NextActiveReservation(context.Background())
	if err != nil || next == nil || next.ID != created.ID {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	plan := recording.FilePlan{PartialPath: "2026/08/attempt.ts.partial", FinalPath: "2026/08/attempt.ts"}
	now := reservation.CreatedAt.Add(time.Minute)
	claim := recording.ClaimRequest{
		ReservationID: reservation.ID, AttemptID: testID(t, 118), SegmentID: testID(t, 119),
		OwnerID: testID(t, 120), OwnerGeneration: 1, Now: now, Plan: plan,
	}
	attempt, err := store.ClaimRecording(context.Background(), claim)
	if err != nil || attempt.State != recording.AttemptClaimed || attempt.Plan != plan ||
		!attempt.PlannedStart.Equal(reservation.Program.Start) {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	if next, err := store.NextActiveReservation(context.Background()); err != nil || next != nil {
		t.Fatalf("claimed next=%+v err=%v", next, err)
	}
	if _, err := store.ClaimRecording(context.Background(), claim); !errors.Is(err, ErrAttemptExists) {
		t.Fatalf("duplicate claim err=%v", err)
	}
	if err := store.StartAttempt(context.Background(), claim.AttemptID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordingStarted(context.Background(), claim.AttemptID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRecordingProgress(context.Background(), claim.AttemptID, 376, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRecordingProgress(context.Background(), claim.AttemptID, 188, now.Add(4*time.Second)); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("減少したbyte数が受理されました: %v", err)
	}
	finalize := recording.FinalizeRequest{
		AttemptID: claim.AttemptID, Token: testID(t, 121), ByteCount: 376, Now: now.Add(5 * time.Second),
	}
	if err := store.BeginFinalization(context.Background(), finalize); err != nil {
		t.Fatal(err)
	}
	premature := recording.FinishRequest{
		AttemptID: claim.AttemptID, State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
		ByteCount: 376, Availability: recording.AvailabilityFinal, Now: now.Add(6 * time.Second),
	}
	if err := store.FinishAttempt(context.Background(), premature); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("公開前の成功が受理されました: %v", err)
	}
	if err := store.MarkFinalPublished(context.Background(), claim.AttemptID, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDirectorySynced(context.Background(), claim.AttemptID, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	premature.Now = now.Add(9 * time.Second)
	if err := store.FinishAttempt(context.Background(), premature); err != nil {
		t.Fatal(err)
	}
	items, err := store.ActiveReservations(context.Background(), 256, 0)
	if err != nil || len(items) != 0 {
		t.Fatalf("active=%+v err=%v", items, err)
	}
	var attemptState, reason, segmentState, availability string
	var byteCount, fileSynced, finalPublished, directorySynced int64
	err = store.reader.QueryRow(`SELECT a.state, a.terminal_reason, a.byte_count, s.state, s.availability,
		s.file_synced, s.final_published, s.directory_synced FROM recording_attempts a
		JOIN recording_segments s ON s.attempt_id=a.id WHERE a.id=?`, claim.AttemptID.Bytes()).Scan(
		&attemptState, &reason, &byteCount, &segmentState, &availability, &fileSynced, &finalPublished, &directorySynced)
	if err != nil || attemptState != "SUCCEEDED" || reason != "COMPLETED" || byteCount != 376 ||
		segmentState != "FINALIZED" || availability != "FINAL" || fileSynced != 1 || finalPublished != 1 || directorySynced != 1 {
		t.Fatalf("state=%s reason=%s bytes=%d segment=%s availability=%s flags=%d/%d/%d err=%v",
			attemptState, reason, byteCount, segmentState, availability, fileSynced, finalPublished, directorySynced, err)
	}
	recoveryItems, err := store.RecoveryAttempts(context.Background(), recording.MaxRecoveryPage, catalogmodel.ID{})
	if err != nil || len(recoveryItems) != 1 || recoveryItems[0].State != recording.AttemptSucceeded ||
		recoveryItems[0].ByteCount != 376 || !recoveryItems[0].FileSynced || !recoveryItems[0].FinalPublished ||
		!recoveryItems[0].DirectorySynced || recoveryItems[0].FinalizationToken != finalize.Token {
		t.Fatalf("recovery=%+v err=%v", recoveryItems, err)
	}
	availabilityAt := now.Add(10 * time.Second)
	if err := store.SetRecordingAvailability(context.Background(), claim.AttemptID, recording.AvailabilityMissing,
		recording.ReasonFileMissing, availabilityAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRecordingAvailability(context.Background(), claim.AttemptID, recording.AvailabilityMissing,
		recording.ReasonFileMissing, availabilityAt); err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err := store.reader.QueryRow(`SELECT availability, integrity_reason FROM recording_segments WHERE attempt_id=?`,
		claim.AttemptID.Bytes()).Scan(&availability, &integrity); err != nil || availability != "MISSING" || integrity != "FILE_MISSING" {
		t.Fatalf("availability=%s integrity=%s err=%v", availability, integrity, err)
	}
}

func TestRecordingAttemptCanFinishWithoutOpeningStream(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	if _, err := store.CreateReservation(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	now := reservation.CreatedAt.Add(time.Minute)
	claim := recording.ClaimRequest{
		ReservationID: reservation.ID, AttemptID: testID(t, 122), SegmentID: testID(t, 123),
		OwnerID: testID(t, 124), OwnerGeneration: 1, Now: now,
		Plan: recording.FilePlan{PartialPath: "2026/08/missed.ts.partial", FinalPath: "2026/08/missed.ts"},
	}
	if _, err := store.ClaimRecording(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	finish := recording.FinishRequest{
		AttemptID: claim.AttemptID, State: recording.AttemptMissed, Reason: recording.ReasonLateStartExpired,
		Availability: recording.AvailabilityMissing, Recovered: true, Now: now.Add(time.Second),
	}
	if err := store.FinishAttempt(context.Background(), finish); err != nil {
		t.Fatal(err)
	}
	var attemptState, reservationState, availability string
	var recovered int
	err := store.reader.QueryRow(`SELECT a.state, r.state, s.availability, a.recovered FROM recording_attempts a
		JOIN reservations r ON r.id=a.reservation_id JOIN recording_segments s ON s.attempt_id=a.id
		WHERE a.id=?`, claim.AttemptID.Bytes()).Scan(&attemptState, &reservationState, &availability, &recovered)
	if err != nil || attemptState != "MISSED" || reservationState != "FINISHED" || availability != "MISSING" || recovered != 1 {
		t.Fatalf("attempt=%s reservation=%s availability=%s recovered=%d err=%v",
			attemptState, reservationState, availability, recovered, err)
	}
}

func TestRecordingClaimRaceOpensOnlyOneAttempt(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	if _, err := store.CreateReservation(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	now := reservation.CreatedAt.Add(time.Minute)
	requests := []recording.ClaimRequest{
		{
			ReservationID: reservation.ID, AttemptID: testID(t, 140), SegmentID: testID(t, 141),
			OwnerID: testID(t, 142), OwnerGeneration: 1, Now: now,
			Plan: recording.FilePlan{PartialPath: "2026/08/race-a.ts.partial", FinalPath: "2026/08/race-a.ts"},
		},
		{
			ReservationID: reservation.ID, AttemptID: testID(t, 143), SegmentID: testID(t, 144),
			OwnerID: testID(t, 145), OwnerGeneration: 1, Now: now,
			Plan: recording.FilePlan{PartialPath: "2026/08/race-b.ts.partial", FinalPath: "2026/08/race-b.ts"},
		},
	}
	errorsFound := make(chan error, len(requests))
	for _, request := range requests {
		request := request
		go func() {
			_, err := store.ClaimRecording(context.Background(), request)
			errorsFound <- err
		}()
	}
	succeeded, duplicate := 0, 0
	for range requests {
		err := <-errorsFound
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAttemptExists):
			duplicate++
		default:
			t.Fatalf("unexpected error=%v", err)
		}
	}
	if succeeded != 1 || duplicate != 1 {
		t.Fatalf("succeeded=%d duplicate=%d", succeeded, duplicate)
	}
	var attempts int
	if err := store.reader.QueryRow(`SELECT count(*) FROM recording_attempts WHERE reservation_id=?`,
		reservation.ID.Bytes()).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func reservationForTest(t *testing.T, store *Store) recording.Reservation {
	t.Helper()
	backendID := testID(t, 110)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "MIRAKURUN", IdentityHash: sha256.Sum256([]byte("mirakurun:reservation")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	syncID := testID(t, 111)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: 2, CorrelationID: "reservation-test",
	}); err != nil {
		t.Fatal(err)
	}
	networkID, transportID, serviceID := int64(1), int64(2), int64(3)
	tuningTarget := "1003"
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: tuningTarget, NetworkID: &networkID, TransportID: &transportID, ServiceID: &serviceID,
		DisplayName: "テスト局", TuningTarget: &tuningTarget, Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	startMS, durationMS, eventID := start.UnixMilli(), int64((30*time.Minute)/time.Millisecond), int64(4)
	title := "テスト番組"
	if err := store.StorePrograms(context.Background(), syncID, false, []catalogmodel.ProgramObservation{{
		ServiceLocator: tuningTarget, EventLocator: "4", RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &startMS, DurationMS: &durationMS,
			Title: &title, Validation: catalogmodel.ValidationValid},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, startMS, 1, 1); err != nil {
		t.Fatal(err)
	}
	programs, err := store.CurrentPrograms(context.Background(), backendID, 1, catalogmodel.ID{})
	if err != nil || len(programs) != 1 {
		t.Fatalf("programs=%+v err=%v", programs, err)
	}
	created := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	return recording.Reservation{
		ID: testID(t, 116), Version: 1, State: recording.ReservationActive, Priority: 5,
		RequestedFollow: true, CreatedAt: created, UpdatedAt: created,
		Program: recording.ProgramSnapshot{
			ProgramInstanceID: programs[0].InstanceID, ProgramRevisionID: programs[0].RevisionID, BackendID: backendID,
			ProviderServiceLocator: tuningTarget, TuningTarget: tuningTarget, NetworkID: uint16(networkID),
			TransportStreamID: uint16(transportID), ServiceID: uint16(serviceID), EventID: uint16(eventID),
			Title: title, StationName: "テスト局", Start: start, Duration: 30 * time.Minute,
		},
	}
}
