package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestReservationCreateReadbackAndDuplicate(t *testing.T) {
	root, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	reservation.Output = recording.OutputSettings{Folder: "ドラマ/保存", Template: "$Title$-$ReserveID$"}
	reservation.Components = recording.ComponentDataOnly
	reservation.PostRecording = recording.PostRecordingSettings{
		Mode: recording.PostRecordingNothing, Script: "/data/post-recording-scripts/after.sh",
	}
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil || created.Number != 1 {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].ID != reservation.ID || items[0].Number != created.Number ||
		!items[0].RequestedFollow || !items[0].EffectiveFollow || items[0].Program.Title != reservation.Program.Title ||
		items[0].Output != reservation.Output || items[0].Components != recording.ComponentDataOnly ||
		items[0].PostRecording != reservation.PostRecording {
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
	if err != nil || len(items) != 1 || items[0].Number != 1 || items[0].Output != reservation.Output ||
		items[0].Components != recording.ComponentDataOnly || items[0].PostRecording != reservation.PostRecording {
		t.Fatalf("restarted items=%+v err=%v", items, err)
	}
}

func TestPostRecordingModesRoundTripAndSurviveRestart(t *testing.T) {
	root, store := openMigratedStore(t)
	base := reservationForTest(t, store)
	for mode := recording.PostRecordingDefault; mode <= recording.PostRecordingShutdown; mode++ {
		reservation := base
		reservation.ID = testID(t, byte(230+mode))
		reservation.Program.EventID = uint16(100 + mode)
		reservation.PostRecording = recording.PostRecordingSettings{Mode: mode}
		if _, err := store.CreateReservation(context.Background(), reservation); err != nil {
			t.Fatalf("mode=%d err=%v", mode, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	items, err := reopened.ActiveReservations(context.Background(), 256, 0)
	if err != nil || len(items) != 7 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	for index, item := range items {
		if item.PostRecording.Mode != recording.PostRecordingMode(index) {
			t.Fatalf("index=%d mode=%d", index, item.PostRecording.Mode)
		}
	}
}

func TestBasicRecordingSettingsRoundTripAndDisabledExpiration(t *testing.T) {
	_, store := openMigratedStore(t)
	base := reservationForTest(t, store)
	base.ID, base.Program.EventID, base.Disabled = testID(t, 121), 10, true
	first, err := store.CreateReservation(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := base
	secondRequest.ID, secondRequest.Program.EventID = testID(t, 122), 11
	second, err := store.CreateReservation(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	beforeEnd := base.PlannedEnd().Add(-time.Second)
	if next, err := store.NextActiveReservation(context.Background(), beforeEnd); err != nil || next != nil {
		t.Fatalf("disabled next=%+v err=%v", next, err)
	}
	deadline, err := store.NextDisabledReservationDeadline(context.Background(), beforeEnd)
	if err != nil || deadline == nil || !deadline.Equal(base.PlannedEnd()) {
		t.Fatalf("deadline=%v err=%v", deadline, err)
	}
	if expired, err := store.ExpireOneDisabledReservation(context.Background(), beforeEnd); err != nil || expired {
		t.Fatalf("early expired=%v err=%v", expired, err)
	}
	for index := 0; index < 2; index++ {
		if expired, err := store.ExpireOneDisabledReservation(context.Background(), base.PlannedEnd()); err != nil || !expired {
			t.Fatalf("expiration %d=%v err=%v", index, expired, err)
		}
		items, err := store.ActiveReservations(context.Background(), 256, 0)
		if err != nil || len(items) != 1-index {
			t.Fatalf("active after %d=%+v err=%v", index, items, err)
		}
	}
	var reasons int
	if err := store.reader.QueryRow(`SELECT count(*) FROM reservations WHERE terminal_reason='DISABLED_EXPIRED'`).Scan(&reasons); err != nil || reasons != 2 {
		t.Fatalf("reasons=%d err=%v", reasons, err)
	}

	reenable := base
	reenable.ID, reenable.Program.EventID = testID(t, 123), 12
	created, err := store.CreateReservation(context.Background(), reenable)
	if err != nil {
		t.Fatal(err)
	}
	request := recording.ReservationRequest{
		NetworkID: reenable.Program.NetworkID, TransportStreamID: reenable.Program.TransportStreamID,
		ServiceID: reenable.Program.ServiceID, EventID: reenable.Program.EventID, Start: reenable.Program.Start,
		Duration: reenable.Program.Duration, Priority: 1, Margins: &recording.RecordingMargins{},
	}
	changeAt := reenable.Program.Start.Add(reenable.Program.Duration - time.Second)
	if err := store.UpdateReservation(context.Background(), recording.ReservationChange{Number: created.Number, Request: request}, changeAt); err != nil {
		t.Fatal(err)
	}
	items, err := store.ActiveReservations(context.Background(), 256, 0)
	if err != nil || len(items) != 1 || items[0].Disabled || items[0].Priority != 1 || items[0].Margins == nil ||
		*items[0].Margins != (recording.RecordingMargins{}) {
		t.Fatalf("reenabled=%+v err=%v", items, err)
	}
	request.Disabled = true
	if err := store.UpdateReservation(context.Background(), recording.ReservationChange{Number: created.Number, Request: request}, changeAt); err != nil {
		t.Fatal(err)
	}
	request.Disabled = false
	if err := store.UpdateReservation(context.Background(), recording.ReservationChange{Number: created.Number, Request: request},
		reenable.Program.Start.Add(reenable.Program.Duration)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("expired re-enable err=%v", err)
	}
	_ = first
	_ = second
}

func TestNextReservationUsesPriorityThenPlannedStartAndID(t *testing.T) {
	_, store := openMigratedStore(t)
	base := reservationForTest(t, store)
	cases := []struct {
		marker   byte
		eventID  uint16
		priority uint8
		start    time.Time
	}{
		{marker: 126, eventID: 20, priority: 1, start: base.Program.Start.Add(-time.Minute)},
		{marker: 125, eventID: 21, priority: 5, start: base.Program.Start},
		{marker: 124, eventID: 22, priority: 5, start: base.Program.Start},
	}
	for _, item := range cases {
		value := base
		value.ID, value.Program.EventID, value.Priority, value.Program.Start = testID(t, item.marker), item.eventID, item.priority, item.start
		if _, err := store.CreateReservation(context.Background(), value); err != nil {
			t.Fatal(err)
		}
	}
	next, err := store.NextActiveReservation(context.Background(), base.Program.Start)
	if err != nil || next == nil || next.ID != testID(t, 124) {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}

func TestReservationSettingsSchemaRejectsInvalidAndScannerRejectsCorruption(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE reservations SET effective_start_margin_seconds=3601 WHERE id=?`,
		`UPDATE reservations SET effective_start_margin_seconds=0 WHERE id=?`,
		`UPDATE reservations SET duration_seconds=86400 WHERE id=?`,
		`UPDATE reservations SET output_folder=replace(hex(zeroblob(257)), '00', 'a') WHERE id=?`,
		`UPDATE reservations SET output_template=replace(hex(zeroblob(513)), '00', 'a') WHERE id=?`,
		`UPDATE reservations SET component_mode=5 WHERE id=?`,
		`UPDATE reservations SET post_action_mode=2 WHERE id=?`,
		`UPDATE reservations SET post_power_mode=6 WHERE id=?`,
		`UPDATE reservations SET post_action_mode=1, post_power_mode=1 WHERE id=?`,
		`UPDATE reservations SET post_script_path=replace(hex(zeroblob(1025)), '00', 'a') WHERE id=?`,
	} {
		if _, err := store.writer.Exec(statement, created.ID.Bytes()); err == nil {
			t.Fatalf("invalid SQL was accepted: %s", statement)
		}
	}
	if _, err := store.writer.Exec(`DROP TRIGGER reservations_basic_settings_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`UPDATE reservations SET effective_start_margin_seconds=0 WHERE id=?`, created.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`UPDATE reservations SET output_folder='../bad' WHERE id=?`, created.ID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActiveReservations(context.Background(), 1, 0); err == nil {
		t.Fatal("corrupt default margin was accepted")
	}
}

func TestExpandedOutputNameFailureRollsBackCreateAndChange(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	reservation.ID = testID(t, 219)
	reservation.Program.EventID = 219
	reservation.Program.Title = strings.Repeat("長", 100)
	reservation.Output = recording.OutputSettings{Template: "$Title$"}
	if _, err := store.CreateReservation(context.Background(), reservation); err == nil {
		t.Fatal("展開後の上限を超える予約を作成しました")
	}
	var created int
	if err := store.reader.QueryRow(`SELECT count(*) FROM reservations WHERE id=?`, reservation.ID.Bytes()).Scan(&created); err != nil || created != 0 {
		t.Fatalf("created=%d err=%v", created, err)
	}

	reservation.Output = recording.OutputSettings{}
	stored, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	change := recording.ReservationChange{Number: stored.Number, Request: recording.ReservationRequest{
		NetworkID: reservation.Program.NetworkID, TransportStreamID: reservation.Program.TransportStreamID,
		ServiceID: reservation.Program.ServiceID, EventID: reservation.Program.EventID,
		Start: reservation.Program.Start, Duration: reservation.Program.Duration, Priority: reservation.Priority,
		RequestedFollow: reservation.RequestedFollow, Output: recording.OutputSettings{Template: "$Title$"},
	}}
	if err := store.UpdateReservation(context.Background(), change, reservation.CreatedAt.Add(time.Second)); err == nil {
		t.Fatal("展開後の上限を超える設定へ変更しました")
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Output != (recording.OutputSettings{}) || items[0].Version != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
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
				endMS != reservation.Program.Start.Add(40*time.Minute+recording.DefaultEndMargin).UnixMilli() {
				t.Fatalf("state=%s version=%d end=%d", persistedState, stateVersion, endMS)
			}
			if state == recording.AttemptRecording {
				end, err := store.UpdateRecordingProgress(context.Background(), attemptID, 188, claim.Now.Add(4*time.Second))
				if err != nil || !end.Equal(reservation.Program.Start.Add(40*time.Minute+recording.DefaultEndMargin)) {
					t.Fatalf("progress end=%s err=%v", end, err)
				}
			}
		})
	}
}

func TestReservationFollowReconcilesActiveTimeUnlessExtensionOnlyOrFinalizing(t *testing.T) {
	cases := []struct {
		name          string
		startShift    time.Duration
		duration      time.Duration
		extensionOnly bool
		finalizing    bool
		stopRequested bool
		wantApplied   bool
	}{
		{name: "shorter", duration: 20 * time.Minute, stopRequested: true, wantApplied: true},
		{name: "shifted start", startShift: 5 * time.Minute, duration: 35 * time.Minute, wantApplied: true},
		{name: "past end", startShift: -6 * time.Hour, duration: time.Minute, wantApplied: true},
		{name: "extension only shorter", duration: 20 * time.Minute, extensionOnly: true},
		{name: "extension only shifted", startShift: 5 * time.Minute, duration: 35 * time.Minute, extensionOnly: true},
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
			if test.stopRequested {
				result, err := store.StopReservation(context.Background(), created.Number, now.Add(3*time.Second))
				if err != nil || !result.Notify || result.ReservationID != created.ID {
					t.Fatalf("stop=%+v err=%v", result, err)
				}
			}
			if test.finalizing {
				if _, err := store.UpdateRecordingProgress(context.Background(), attemptID, 188, now.Add(3*time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := store.BeginFinalization(context.Background(), recording.FinalizeRequest{
					AttemptID: attemptID, Token: testID(t, byte(200+index)), ByteCount: 188,
					State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted, Now: now.Add(4 * time.Second),
				}); err != nil {
					t.Fatal(err)
				}
			}
			target := storeFollowRevision(t, store, reservation, reservation.Program.Start.Add(test.startShift),
				test.duration, byte(210+index))
			applied, err := store.ApplyReservationFollow(context.Background(), recording.ReservationFollowRequest{
				ReservationID: created.ID, ExpectedVersion: created.Version,
				ExpectedRevisionID: created.Program.ProgramRevisionID, TargetRevisionID: target.ProgramRevisionID,
				Now: now.Add(5 * time.Second), ExtensionOnly: test.extensionOnly,
			})
			if err != nil || applied != test.wantApplied {
				t.Fatalf("applied=%v want=%v err=%v", applied, test.wantApplied, err)
			}
			items, err := store.ActiveReservations(context.Background(), 1, 0)
			if err != nil || len(items) != 1 {
				t.Fatalf("items=%+v err=%v", items, err)
			}
			expectedVersion := int64(1)
			expectedRevision := reservation.Program.ProgramRevisionID
			expectedStart := reservation.Program.Start
			expectedDuration := reservation.Program.Duration
			expectedEnd := reservation.PlannedEnd()
			if test.wantApplied {
				expectedVersion = 2
				expectedRevision = target.ProgramRevisionID
				expectedStart = target.Start
				expectedDuration = target.Duration
				expectedEnd = target.Start.Add(target.Duration + recording.DefaultEndMargin)
				if !expectedEnd.After(reservation.PlannedStart()) {
					expectedEnd = reservation.PlannedStart().Add(time.Millisecond)
				}
			}
			if items[0].Version != expectedVersion || items[0].Program.ProgramRevisionID != expectedRevision ||
				!items[0].Program.Start.Equal(expectedStart) || items[0].Program.Duration != expectedDuration {
				t.Fatalf("items=%+v expected_version=%d expected_start=%s expected_duration=%s",
					items, expectedVersion, expectedStart, expectedDuration)
			}
			var startMS, endMS, actualStartMS, stopMS, byteCount int64
			var partialPath, finalPath string
			if err := store.reader.QueryRow(`SELECT a.planned_start_utc_ms, a.planned_end_utc_ms,
				a.actual_start_utc_ms, COALESCE(a.stop_requested_at_utc_ms, -1), a.byte_count,
				s.relative_partial_path, s.relative_final_path
				FROM recording_attempts a JOIN recording_segments s ON s.attempt_id=a.id
				WHERE a.id=?`, attemptID.Bytes()).Scan(&startMS, &endMS, &actualStartMS, &stopMS, &byteCount,
				&partialPath, &finalPath); err != nil {
				t.Fatal(err)
			}
			expectedBytes := int64(0)
			if test.finalizing {
				expectedBytes = 188
			}
			expectedStopMS := int64(-1)
			if test.stopRequested {
				expectedStopMS = now.Add(3 * time.Second).UnixMilli()
			}
			if startMS != reservation.PlannedStart().UnixMilli() || endMS != expectedEnd.UnixMilli() ||
				actualStartMS != now.Add(2*time.Second).UnixMilli() || stopMS != expectedStopMS ||
				byteCount != expectedBytes || partialPath != plan.PartialPath || finalPath != plan.FinalPath {
				t.Fatalf("start=%d end=%d actual_start=%d stop=%d bytes=%d partial=%q final=%q expected_end=%d",
					startMS, endMS, actualStartMS, stopMS, byteCount, partialPath, finalPath, expectedEnd.UnixMilli())
			}
		})
	}
}

func TestActiveFollowTimeBounds(t *testing.T) {
	if !validFollowRecordingTime(3_600_000, 60, 5, 2) ||
		validFollowRecordingTime(3_600_000, 60, -30, -30) ||
		!validFollowRecordingTime(3_600_000, 79_200, 3_600, 3_600) ||
		validFollowRecordingTime(3_600_000, 79_201, 3_600, 3_600) {
		t.Fatal("実効録画時間の境界判定が一致しません")
	}
	if end, ok := activeFollowEnd(0, 999, 0, 1_000); !ok || end != 1_001 {
		t.Fatalf("clamped end=%d ok=%v", end, ok)
	}
	maximum := recording.MaxEffectiveDuration.Milliseconds()
	if end, ok := activeFollowEnd(1_000, maximum, 0, 1_000); !ok || end != 1_000+maximum {
		t.Fatalf("maximum end=%d ok=%v", end, ok)
	}
	if _, ok := activeFollowEnd(1_000, maximum+1, 0, 1_000); ok {
		t.Fatal("24時間を超える終了予定を受け入れました")
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
	if err := store.CompleteSync(context.Background(), syncID, reservation.Program.Start.UnixMilli()+2, 1, 1); err != nil {
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
		Output:     recording.OutputSettings{Folder: "更新後", Template: "$SDYYYY$-$Title$"},
		Components: recording.ComponentCaptionsOnly,
	}}
	now := reservation.CreatedAt.Add(time.Second)
	if err := store.UpdateReservation(context.Background(), change, now); err != nil {
		t.Fatal(err)
	}
	items, err := store.ActiveReservations(context.Background(), 1, 0)
	if err != nil || len(items) != 1 || items[0].Priority != 5 || items[0].Version != 2 || items[0].Output != change.Request.Output ||
		items[0].Components != recording.ComponentCaptionsOnly {
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

func TestStopReservationCancelsBeforeRecordingWithoutNotification(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.StopReservation(context.Background(), created.Number, reservation.CreatedAt.Add(time.Second))
	if err != nil || result.Notify || result.ReservationID != (catalogmodel.ID{}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := store.StopReservation(context.Background(), created.Number, reservation.CreatedAt.Add(2*time.Second)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("終了済み予約の停止が成功しました: %v", err)
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
	next, err := store.NextActiveReservation(context.Background(), reservation.CreatedAt)
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
		!attempt.PlannedStart.Equal(reservation.Program.Start.Add(-recording.DefaultStartMargin)) ||
		!attempt.PlannedEnd.Equal(reservation.Program.Start.Add(reservation.Program.Duration).Add(recording.DefaultEndMargin)) {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
	if next, err := store.NextActiveReservation(context.Background(), reservation.CreatedAt); err != nil || next != nil {
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
		AttemptID: claim.AttemptID, Token: testID(t, 121), ByteCount: 376,
		State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted, Now: now.Add(5 * time.Second),
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
	history, err := store.RecordingHistory(context.Background(), 1, 0)
	if err != nil || len(history) != 1 || history[0].Number != created.Number || !history[0].Playable() ||
		history[0].Plan != plan || history[0].ByteCount != 376 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	completed, err := store.CompletedRecordings(context.Background(), 1, 0)
	if err != nil || len(completed) != 1 || completed[0].Number != created.Number {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	item, err := store.RecordingHistoryItem(context.Background(), created.Number)
	if err != nil || item == nil || !item.Playable() {
		t.Fatalf("item=%+v err=%v", item, err)
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
	history, err = store.RecordingHistory(context.Background(), 1, 0)
	if err != nil || len(history) != 1 || history[0].Playable() {
		t.Fatalf("missing history=%+v err=%v", history, err)
	}
	completed, err = store.CompletedRecordings(context.Background(), 1, 0)
	if err != nil || len(completed) != 0 {
		t.Fatalf("missing completed=%+v err=%v", completed, err)
	}
	if _, err := store.writer.ExecContext(context.Background(), `UPDATE reservations SET network_id=70000 WHERE id=?`, reservation.ID.Bytes()); err == nil {
		t.Fatal("範囲外の履歴値がDBへ保存されました")
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
	history, err := store.RecordingHistory(context.Background(), 1, 0)
	if err != nil || len(history) != 1 || history[0].State != recording.AttemptMissed || history[0].Playable() {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	completed, err := store.CompletedRecordings(context.Background(), 1, 0)
	if err != nil || len(completed) != 0 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	var attemptState, reservationState, availability string
	var recovered int
	err = store.reader.QueryRow(`SELECT a.state, r.state, s.availability, a.recovered FROM recording_attempts a
		JOIN reservations r ON r.id=a.reservation_id JOIN recording_segments s ON s.attempt_id=a.id
		WHERE a.id=?`, claim.AttemptID.Bytes()).Scan(&attemptState, &reservationState, &availability, &recovered)
	if err != nil || attemptState != "MISSED" || reservationState != "FINISHED" || availability != "MISSING" || recovered != 1 {
		t.Fatalf("attempt=%s reservation=%s availability=%s recovered=%d err=%v",
			attemptState, reservationState, availability, recovered, err)
	}
}

func TestUserStopIsPersistedIdempotentlyAndPublishesOnlyItsPartialRecording(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	now := reservation.CreatedAt.Add(time.Minute)
	plan := recording.FilePlan{PartialPath: "2026/08/stopped.ts.partial", FinalPath: "2026/08/stopped.ts"}
	claim := recording.ClaimRequest{
		ReservationID: created.ID, AttemptID: testID(t, 131), SegmentID: testID(t, 132),
		OwnerID: testID(t, 133), OwnerGeneration: 1, Now: now, Plan: plan,
	}
	if _, err := store.ClaimRecording(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err := store.StartAttempt(context.Background(), claim.AttemptID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stopAt := now.Add(2 * time.Second)
	for index := 0; index < 2; index++ {
		result, err := store.StopReservation(context.Background(), created.Number, stopAt.Add(time.Duration(index)*time.Second))
		if err != nil || !result.Notify || result.ReservationID != created.ID {
			t.Fatalf("stop=%+v err=%v", result, err)
		}
	}
	requested, err := store.AttemptStopRequested(context.Background(), claim.AttemptID)
	if err != nil || !requested {
		t.Fatalf("requested=%v err=%v", requested, err)
	}
	if _, err := store.RecordingStarted(context.Background(), claim.AttemptID, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateRecordingProgress(context.Background(), claim.AttemptID, 376, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	normal := recording.FinalizeRequest{
		AttemptID: claim.AttemptID, Token: testID(t, 134), ByteCount: 376,
		State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted, Now: now.Add(6 * time.Second),
	}
	if err := store.BeginFinalization(context.Background(), normal); !errors.Is(err, ErrAttemptState) {
		t.Fatalf("停止要求後の正常完成が受理されました: %v", err)
	}
	stopped := normal
	stopped.Token = testID(t, 135)
	stopped.State = recording.AttemptPartial
	stopped.Reason = recording.ReasonUserRequestedStop
	if err := store.BeginFinalization(context.Background(), stopped); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StopReservation(context.Background(), created.Number, now.Add(7*time.Second)); !errors.Is(err, ErrReservationUnavailable) {
		t.Fatalf("完成処理中の停止が成功しました: %v", err)
	}
	if err := store.MarkFinalPublished(context.Background(), claim.AttemptID, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDirectorySynced(context.Background(), claim.AttemptID, now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	finish := recording.FinishRequest{
		AttemptID: claim.AttemptID, State: recording.AttemptPartial, Reason: recording.ReasonUserRequestedStop,
		ByteCount: 376, Availability: recording.AvailabilityFinal, Now: now.Add(10 * time.Second),
	}
	if err := store.FinishAttempt(context.Background(), finish); err != nil {
		t.Fatal(err)
	}
	history, err := store.RecordingHistoryItem(context.Background(), created.Number)
	if err != nil || history == nil || !history.Playable() || history.State != recording.AttemptPartial ||
		history.Reason != recording.ReasonUserRequestedStop {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	completed, err := store.CompletedRecordings(context.Background(), 1, 0)
	if err != nil || len(completed) != 1 || completed[0].Number != created.Number {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	recoveryItems, err := store.RecoveryAttempts(context.Background(), recording.MaxRecoveryPage, catalogmodel.ID{})
	if err != nil || len(recoveryItems) != 1 || recoveryItems[0].PlannedState != recording.AttemptPartial ||
		recoveryItems[0].PlannedReason != recording.ReasonUserRequestedStop {
		t.Fatalf("recovery=%+v err=%v", recoveryItems, err)
	}
}

func TestConcurrentUserStopRequestsConvergeOnOneTimestamp(t *testing.T) {
	_, store := openMigratedStore(t)
	reservation := reservationForTest(t, store)
	created, err := store.CreateReservation(context.Background(), reservation)
	if err != nil {
		t.Fatal(err)
	}
	now := reservation.CreatedAt.Add(time.Minute)
	claim := recording.ClaimRequest{
		ReservationID: created.ID, AttemptID: testID(t, 136), SegmentID: testID(t, 137),
		OwnerID: testID(t, 138), OwnerGeneration: 1, Now: now,
		Plan: recording.FilePlan{PartialPath: "2026/08/concurrent.ts.partial", FinalPath: "2026/08/concurrent.ts"},
	}
	if _, err := store.ClaimRecording(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	const requests = 16
	errorsFound := make(chan error, requests)
	var group sync.WaitGroup
	for index := 0; index < requests; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, stopErr := store.StopReservation(context.Background(), created.Number, now.Add(time.Second))
			if stopErr != nil {
				errorsFound <- stopErr
				return
			}
			if !result.Notify || result.ReservationID != created.ID {
				errorsFound <- errors.New("unexpected stop result")
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for stopErr := range errorsFound {
		t.Fatal(stopErr)
	}
	var requestedAt, version int64
	if err := store.reader.QueryRow(`SELECT stop_requested_at_utc_ms, state_version FROM recording_attempts WHERE id=?`,
		claim.AttemptID.Bytes()).Scan(&requestedAt, &version); err != nil || requestedAt != now.Add(time.Second).UnixMilli() || version != 2 {
		t.Fatalf("requested_at=%d version=%d err=%v", requestedAt, version, err)
	}
	if _, err := store.writer.Exec(`UPDATE recording_attempts SET stop_requested_at_utc_ms=? WHERE id=?`,
		now.Add(2*time.Second).UnixMilli(), claim.AttemptID.Bytes()); err == nil {
		t.Fatal("保存済みの停止要求時刻が上書きされました")
	}
	if _, err := store.writer.Exec(`UPDATE recording_attempts SET planned_final_state='PARTIAL' WHERE id=?`,
		claim.AttemptID.Bytes()); err == nil {
		t.Fatal("終了理由のない完成計画が保存されました")
	}
}

func TestRecordingHistoryRejectsUnboundedQueries(t *testing.T) {
	_, store := openMigratedStore(t)
	empty, err := store.RecordingHistory(context.Background(), 1, 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
	for _, limit := range []int{0, recording.MaxHistoryPage + 1} {
		if _, err := store.RecordingHistory(context.Background(), limit, 0); err == nil {
			t.Fatalf("history limit=%d accepted", limit)
		}
		if _, err := store.CompletedRecordings(context.Background(), limit, 0); err == nil {
			t.Fatalf("completed limit=%d accepted", limit)
		}
	}
	if _, err := store.RecordingHistory(context.Background(), 1, -1); err == nil {
		t.Fatal("negative cursor accepted")
	}
	if _, err := store.RecordingHistoryItem(context.Background(), 0); err == nil {
		t.Fatal("zero id accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.RecordingHistory(ctx, 1, 0); err == nil {
		t.Fatal("cancelled query accepted")
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
		Components:      recording.ComponentBoth,
		RequestedFollow: true, CreatedAt: created, UpdatedAt: created,
		Program: recording.ProgramSnapshot{
			ProgramInstanceID: programs[0].InstanceID, ProgramRevisionID: programs[0].RevisionID, BackendID: backendID,
			ProviderServiceLocator: tuningTarget, TuningTarget: tuningTarget, NetworkID: uint16(networkID),
			TransportStreamID: uint16(transportID), ServiceID: uint16(serviceID), EventID: uint16(eventID),
			Title: title, StationName: "テスト局", Start: start, Duration: 30 * time.Minute,
		},
	}
}
