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
		!items[0].RequestedFollow || !items[0].EffectiveFollow || items[0].Program.Title != reservation.Program.Title {
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

func TestReservationFollowUsesLatestCompletedRevision(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	syncID := testID(t, 112)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: reservation.Program.BackendID, StartedAtMS: 3, CorrelationID: "follow-target",
	}); err != nil {
		t.Fatal(err)
	}
	networkID, transportID, serviceID := int64(1), int64(2), int64(3)
	tuningTarget := reservation.Program.TuningTarget
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: tuningTarget, NetworkID: &networkID, TransportID: &transportID, ServiceID: &serviceID,
		DisplayName: "テスト局", TuningTarget: &tuningTarget, Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	start := reservation.Program.Start.Add(10 * time.Minute)
	startMS, durationMS, eventID := start.UnixMilli(), int64((35*time.Minute)/time.Millisecond), int64(4)
	title := "変更後の番組名"
	if err := store.StorePrograms(context.Background(), syncID, false, []catalogmodel.ProgramObservation{{
		ServiceLocator: tuningTarget, EventLocator: "4", RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &startMS, DurationMS: &durationMS,
			Title: &title, Validation: catalogmodel.ValidationValid},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, startMS+1, 1, 1); err != nil {
		t.Fatal(err)
	}
	var programIdentity string
	if err := store.reader.QueryRow(`SELECT identity_state FROM program_instances WHERE id=?`,
		reservation.Program.ProgramInstanceID.Bytes()).Scan(&programIdentity); err != nil {
		t.Fatal(err)
	}
	if programIdentity != "PROVISIONAL" {
		t.Fatalf("program_identity=%s", programIdentity)
	}
	target, err := store.CurrentFollowTarget(context.Background(), reservation.Program.BackendID,
		reservation.Program.ProgramInstanceID)
	if err != nil || target == nil || target.ProgramRevisionID == reservation.Program.ProgramRevisionID ||
		!target.Start.Equal(start) || target.Duration != 35*time.Minute {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	applied, err := store.ApplyReservationFollow(context.Background(), recording.ReservationFollowRequest{
		ReservationID: created.ID, ExpectedVersion: created.Version,
		ExpectedRevisionID: created.Program.ProgramRevisionID, TargetRevisionID: target.ProgramRevisionID,
		Now: time.Date(2026, 8, 5, 0, 10, 0, 0, time.UTC),
	})
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Version != 2 ||
		items[0].Program.ProgramRevisionID != target.ProgramRevisionID || !items[0].Program.Start.Equal(start) ||
		items[0].Program.Duration != 35*time.Minute || items[0].Program.Title != reservation.Program.Title ||
		!items[0].EffectiveFollow {
		t.Fatalf("followed=%+v err=%v", items, err)
	}
	applied, err = store.ApplyReservationFollow(context.Background(), recording.ReservationFollowRequest{
		ReservationID: created.ID, ExpectedVersion: created.Version,
		ExpectedRevisionID: created.Program.ProgramRevisionID, TargetRevisionID: target.ProgramRevisionID,
		Now: time.Date(2026, 8, 5, 0, 11, 0, 0, time.UTC),
	})
	if err != nil || applied {
		t.Fatalf("stale applied=%v err=%v", applied, err)
	}
}

func TestReservationFollowExtendsActiveRecording(t *testing.T) {
	states := []recording.AttemptState{recording.AttemptClaimed, recording.AttemptStarting, recording.AttemptRecording}
	for index, state := range states {
		t.Run(string(state), func(t *testing.T) {
			_, store := openMigratedStore(t)
			defer store.Close()
			reservation := reservationForTest(t, store)
			created, err := store.CreateReservation(context.Background(), reservation)
			if err != nil {
				t.Fatal(err)
			}
			attemptID := testID(t, byte(130+index))
			plan, err := recording.NewFilePlan(reservation.Program.Start, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			claim := recording.ClaimRequest{
				ReservationID: created.ID, AttemptID: attemptID, SegmentID: testID(t, byte(140+index)),
				OwnerID: testID(t, byte(150+index)), OwnerGeneration: 1,
				Now: reservation.CreatedAt.Add(time.Minute), Plan: plan,
			}
			if _, err := store.ClaimRecording(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			if state == recording.AttemptStarting || state == recording.AttemptRecording {
				if err := store.StartAttempt(context.Background(), attemptID, claim.Now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if state == recording.AttemptRecording {
				if _, err := store.RecordingStarted(context.Background(), attemptID, claim.Now.Add(2*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			target := storeFollowRevision(t, store, reservation, reservation.Program.Start, 40*time.Minute,
				byte(160+index))
			applied, err := store.ApplyReservationFollow(context.Background(), recording.ReservationFollowRequest{
				ReservationID: created.ID, ExpectedVersion: created.Version,
				ExpectedRevisionID: created.Program.ProgramRevisionID, TargetRevisionID: target.ProgramRevisionID,
				Now: claim.Now.Add(3 * time.Second),
			})
			if err != nil || !applied {
				t.Fatalf("applied=%v err=%v", applied, err)
			}
			items, err := store.ActiveReservations(context.Background(), 1, 0)
			if err != nil || len(items) != 1 || items[0].Version != 2 ||
				items[0].Program.ProgramRevisionID != target.ProgramRevisionID ||
				items[0].Program.Duration != 40*time.Minute || !items[0].Program.Start.Equal(reservation.Program.Start) {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			var endMS, stateVersion int64
			var persistedState recording.AttemptState
			if err := store.reader.QueryRow(`SELECT state, state_version, planned_end_utc_ms
				FROM recording_attempts WHERE id=?`, attemptID.Bytes()).Scan(&persistedState, &stateVersion, &endMS); err != nil {
				t.Fatal(err)
			}
			if persistedState != state || stateVersion != int64(2+index) ||
				endMS != reservation.Program.Start.Add(40*time.Minute).UnixMilli() {
				t.Fatalf("state=%s version=%d end=%d", persistedState, stateVersion, endMS)
			}
			if state == recording.AttemptRecording {
				end, err := store.UpdateRecordingProgress(context.Background(), attemptID, 188, claim.Now.Add(4*time.Second))
				if err != nil || !end.Equal(reservation.Program.Start.Add(40*time.Minute)) {
					t.Fatalf("progress end=%s err=%v", end, err)
				}
			}
		})
	}
}

func TestReservationFollowDoesNotShortenShiftOrFinalizeActiveRecording(t *testing.T) {
	cases := []struct {
		name       string
		startShift time.Duration
		duration   time.Duration
		finalizing bool
	}{
		{name: "shorter", duration: 20 * time.Minute},
		{name: "shifted start", startShift: 5 * time.Minute, duration: 35 * time.Minute},
		{name: "finalizing", duration: 40 * time.Minute, finalizing: true},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, store := openMigratedStore(t)
			defer store.Close()
			reservation := reservationForTest(t, store)
			created, err := store.CreateReservation(context.Background(), reservation)
			if err != nil {
				t.Fatal(err)
			}
			attemptID := testID(t, byte(170+index))
			plan, err := recording.NewFilePlan(reservation.Program.Start, attemptID)
			if err != nil {
				t.Fatal(err)
			}
			now := reservation.CreatedAt.Add(time.Minute)
			claim := recording.ClaimRequest{
				ReservationID: created.ID, AttemptID: attemptID, SegmentID: testID(t, byte(180+index)),
				OwnerID: testID(t, byte(190+index)), OwnerGeneration: 1, Now: now, Plan: plan,
			}
			if _, err := store.ClaimRecording(context.Background(), claim); err != nil {
				t.Fatal(err)
			}
			if err := store.StartAttempt(context.Background(), attemptID, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RecordingStarted(context.Background(), attemptID, now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if test.finalizing {
				if _, err := store.UpdateRecordingProgress(context.Background(), attemptID, 188, now.Add(3*time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := store.BeginFinalization(context.Background(), recording.FinalizeRequest{
					AttemptID: attemptID, Token: testID(t, byte(200+index)), ByteCount: 188, Now: now.Add(4 * time.Second),
				}); err != nil {
					t.Fatal(err)
				}
			}
			target := storeFollowRevision(t, store, reservation, reservation.Program.Start.Add(test.startShift),
				test.duration, byte(210+index))
			applied, err := store.ApplyReservationFollow(context.Background(), recording.ReservationFollowRequest{
				ReservationID: created.ID, ExpectedVersion: created.Version,
				ExpectedRevisionID: created.Program.ProgramRevisionID, TargetRevisionID: target.ProgramRevisionID,
				Now: now.Add(5 * time.Second),
			})
			if err != nil || applied {
				t.Fatalf("applied=%v err=%v", applied, err)
			}
			items, err := store.ActiveReservations(context.Background(), 1, 0)
			if err != nil || len(items) != 1 || items[0].Version != 1 ||
				items[0].Program.ProgramRevisionID != reservation.Program.ProgramRevisionID ||
				items[0].Program.Duration != reservation.Program.Duration {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			var endMS int64
			if err := store.reader.QueryRow(`SELECT planned_end_utc_ms FROM recording_attempts WHERE id=?`,
				attemptID.Bytes()).Scan(&endMS); err != nil {
				t.Fatal(err)
			}
			if endMS != reservation.Program.Start.Add(reservation.Program.Duration).UnixMilli() {
				t.Fatalf("end=%d", endMS)
			}
		})
	}
}

func storeFollowRevision(t *testing.T, store *Store, reservation recording.Reservation, start time.Time,
	duration time.Duration, idByte byte,
) recording.FollowTarget {
	t.Helper()
	syncID := testID(t, idByte)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: reservation.Program.BackendID, StartedAtMS: 3, CorrelationID: "active-follow-target",
	}); err != nil {
		t.Fatal(err)
	}
	networkID, transportID, serviceID := int64(1), int64(2), int64(3)
	tuningTarget := reservation.Program.TuningTarget
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: tuningTarget, NetworkID: &networkID, TransportID: &transportID, ServiceID: &serviceID,
		DisplayName: "テスト局", TuningTarget: &tuningTarget, Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	startMS, durationMS, eventID := start.UnixMilli(), duration.Milliseconds(), int64(reservation.Program.EventID)
	title := "延長後"
	if err := store.StorePrograms(context.Background(), syncID, false, []catalogmodel.ProgramObservation{{
		ServiceLocator: tuningTarget, EventLocator: "4", RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &startMS, DurationMS: &durationMS,
			Title: &title, Validation: catalogmodel.ValidationValid},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, startMS+1, 1, 1); err != nil {
		t.Fatal(err)
	}
	target, err := store.CurrentFollowTarget(context.Background(), reservation.Program.BackendID,
		reservation.Program.ProgramInstanceID)
	if err != nil || target == nil {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	return *target
}

func TestReservationUpdateCancelAndRecordingStatus(t *testing.T) {
	root, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	change := recording.ReservationChange{Number: created.Number, Request: recording.ReservationRequest{
		NetworkID: reservation.Program.NetworkID, TransportStreamID: reservation.Program.TransportStreamID,
		ServiceID: reservation.Program.ServiceID, EventID: reservation.Program.EventID,
		Start: reservation.Program.Start, Duration: reservation.Program.Duration, Priority: 5,
	}}
	now := reservation.CreatedAt.Add(time.Second)
	if err := store.UpdateReservation(context.Background(), change, now); err != nil {
		t.Fatal(err)
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Priority != 5 || items[0].Version != 2 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	change.Request.EventID++
	if err := store.UpdateReservation(context.Background(), change, now.Add(time.Second)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("変更不可項目の差分が受理されました: %v", err)
	}
	active, err := store.ReservationRecording(context.Background(), created.Number)
	if err != nil || active {
		t.Fatalf("recording=%v err=%v", active, err)
	}
	if err := store.CancelReservation(context.Background(), created.Number, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelReservation(context.Background(), created.Number, now.Add(3*time.Second)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("取消しの再送が成功しました: %v", err)
	}
	var state, reason string
	if err := store.reader.QueryRow(`SELECT state, terminal_reason FROM reservations WHERE id=?`, reservation.ID.Bytes()).Scan(&state, &reason); err != nil ||
		state != "FINISHED" || reason != "CANCELLED_BY_USER" {
		t.Fatalf("state=%s reason=%s err=%v", state, reason, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err = reopened.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 0 {
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

func TestGenerationQueriesPinCompletedAndAllowOnlyRunningCandidateServices(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	backendID := reservation.Program.BackendID
	completedID, err := store.LatestCompletedGeneration(context.Background(), backendID)
	if err != nil || completedID != testID(t, 111) {
		t.Fatalf("completed=%s err=%v", completedID.String(), err)
	}
	services, err := store.ServicesForGeneration(context.Background(), backendID, completedID,
		catalogmodel.GenerationCompleted, 16, catalogmodel.ID{})
	if err != nil || len(services) != 1 || services[0].ProviderLocator != reservation.Program.ProviderServiceLocator {
		t.Fatalf("completed services=%+v err=%v", services, err)
	}
	programs, err := store.ProgramsByServiceForGeneration(context.Background(), backendID, completedID,
		16, catalogmodel.ProgramCursor{})
	if err != nil || len(programs) != 1 {
		t.Fatalf("completed programs=%+v err=%v", programs, err)
	}
	selected, err := store.ProgramsForServiceForGeneration(context.Background(), backendID, completedID,
		reservation.Program.ProviderServiceLocator, 16, "")
	if err != nil || len(selected) != 1 {
		t.Fatalf("service programs=%+v err=%v", selected, err)
	}
	matches, err := store.ProgramsMatchingGeneration(context.Background(), backendID, completedID,
		reservation.Program.ProviderServiceLocator, int64(reservation.Program.EventID),
		reservation.Program.Start.UnixMilli(), reservation.Program.Duration.Milliseconds())
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%+v err=%v", matches, err)
	}

	runningID := testID(t, 112)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: runningID, BackendID: backendID, StartedAtMS: reservation.Program.Start.UnixMilli() + 1,
		CorrelationID: "running-candidate",
	}); err != nil {
		t.Fatal(err)
	}
	networkID, transportID, serviceID := int64(1), int64(2), int64(3)
	if err := store.StoreServices(context.Background(), runningID, []catalogmodel.ServiceObservation{{
		ProviderLocator: reservation.Program.ProviderServiceLocator, NetworkID: &networkID,
		TransportID: &transportID, ServiceID: &serviceID, DisplayName: "候補局",
		Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.ServicesForGeneration(context.Background(), backendID, runningID,
		catalogmodel.GenerationRunning, 16, catalogmodel.ID{})
	if err != nil || len(candidate) != 1 || candidate[0].DisplayName != "候補局" {
		t.Fatalf("candidate=%+v err=%v", candidate, err)
	}
	notPublished, err := store.ServicesForGeneration(context.Background(), backendID, runningID,
		catalogmodel.GenerationCompleted, 16, catalogmodel.ID{})
	if err != nil || len(notPublished) != 0 {
		t.Fatalf("running was published: %+v err=%v", notPublished, err)
	}
	if latest, err := store.LatestCompletedGeneration(context.Background(), backendID); err != nil || latest != completedID {
		t.Fatalf("latest=%s err=%v", latest.String(), err)
	}
	if _, err := store.ServicesForGeneration(context.Background(), backendID, runningID, 0, 16, catalogmodel.ID{}); err == nil {
		t.Fatal("invalid generation state was accepted")
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
	active, err := store.ReservationRecording(context.Background(), created.Number)
	if err != nil || !active {
		t.Fatalf("starting recording=%v err=%v", active, err)
	}
	change := recording.ReservationChange{Number: created.Number, Request: recording.ReservationRequest{
		NetworkID: reservation.Program.NetworkID, TransportStreamID: reservation.Program.TransportStreamID,
		ServiceID: reservation.Program.ServiceID, EventID: reservation.Program.EventID,
		Start: reservation.Program.Start, Duration: reservation.Program.Duration, Priority: 5,
	}}
	if err := store.UpdateReservation(context.Background(), change, now.Add(time.Second)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("録画開始後の変更が成功しました: %v", err)
	}
	if err := store.CancelReservation(context.Background(), created.Number, now.Add(time.Second)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("録画開始後の取消しが成功しました: %v", err)
	}
	if _, err := store.RecordingStarted(context.Background(), claim.AttemptID, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRecordingProgress(context.Background(), claim.AttemptID, 376, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRecordingProgress(context.Background(), claim.AttemptID, 188, now.Add(4*time.Second)); !errors.Is(err, ErrAttemptState) {
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
	var attemptState, reason, segmentState, availability, reservationReason string
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
	if err := store.reader.QueryRow(`SELECT terminal_reason FROM reservations WHERE id=?`, reservation.ID.Bytes()).Scan(&reservationReason); err != nil ||
		reservationReason != "ATTEMPT_FINISHED" {
		t.Fatalf("reservation reason=%s err=%v", reservationReason, err)
	}
	active, err = store.ReservationRecording(context.Background(), created.Number)
	if err != nil || active {
		t.Fatalf("finished recording=%v err=%v", active, err)
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
