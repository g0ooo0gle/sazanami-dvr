package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func qualityRunningAttempt(t *testing.T, store *Store) (core.ClaimRequest, core.Reservation, time.Time) {
	t.Helper()
	ctx := context.Background()
	r := reservationForTest(t, store)
	r.OneSegOutput = &core.OneSegOutput{ProviderServiceLocator: "1004"}
	r, err := store.CreateReservation(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	now := r.Program.Start
	plan, err := core.NewOneSegFilePlan(r, testID(t, 200))
	if err != nil {
		t.Fatal(err)
	}
	claim := core.ClaimRequest{ReservationID: r.ID, ReservationVersion: r.Version,
		AttemptID: testID(t, 200), SegmentID: testID(t, 201), OneSegSegmentID: testID(t, 202),
		OwnerID: testID(t, 203), OwnerGeneration: 1, Now: now,
		Plan: core.FilePlan{PartialPath: "2026/08/main.ts.partial", FinalPath: "2026/08/main.ts"}, OneSegPlan: &plan}
	if _, err := store.ClaimRecording(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := store.StartAttempt(ctx, claim.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordingStarted(ctx, claim.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.OneSegRecordingStarted(ctx, claim.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	return claim, r, now
}

func TestQualityPersistenceRoundTrip(t *testing.T) {
	root, store := openMigratedStore(t)
	claim, r, now := qualityRunningAttempt(t, store)
	main := core.QualitySummary{Status: core.QualityDegraded, SelectionUnverified: true, ObservationLimited: true,
		CountersSaturated: true, CCGapEvents: 2147483647, CCDuplicateEvents: 2, TEIPackets: 3,
		MalformedPacketEvents: 4, PSIContinuityEvents: 5, PSICRCEvents: 6, PSIStructureEvents: 7,
		PSILimitEvents: 8, SyncLossEvents: 9, SyncRecoveredEvents: 10, SyncDiscardedBytes: 11,
		TrailingIncompleteBytes: 12, UnfinishedPSIEvents: 13, FallbackEvents: 1, ReconnectCount: 3}
	one := core.QualitySummary{Status: core.QualityNoIssuesObserved}
	ctx := context.Background()
	if _, err := store.UpdateRecordingProgress(ctx, claim.AttemptID, 376, now.Add(time.Second), main); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateOneSegProgress(ctx, claim.AttemptID, 188, now.Add(time.Second), one); err != nil {
		t.Fatal(err)
	}
	invalid := main
	invalid.CCGapEvents = -1
	if _, err := store.UpdateRecordingProgress(ctx, claim.AttemptID, 564, now.Add(2*time.Second), invalid); err == nil {
		t.Fatal("不正な品質が進捗へ保存されました")
	}
	items, err := store.RecoveryAttempts(ctx, core.MaxRecoveryPage, catalogmodel.ID{})
	if err != nil || len(items) != 1 || items[0].Quality != main || items[0].ByteCount != 376 ||
		items[0].OneSeg == nil || items[0].OneSeg.Quality != one {
		t.Fatalf("進捗quality=%+v err=%v", items, err)
	}
	request := core.FinalizeRequest{AttemptID: claim.AttemptID, Token: testID(t, 204), ByteCount: 376,
		State: core.AttemptPartial, Reason: core.ReasonStreamTimeout, Now: now.Add(2 * time.Second), Quality: main,
		OneSeg: &core.OneSegResult{ByteCount: 188, Availability: core.AvailabilityPartial,
			Reason: core.ReasonCompleted, FileSynced: true, Publish: true, Quality: one}}
	if _, err := store.BeginFinalization(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFinalPublished(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDirectorySynced(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOneSegFinalPublished(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOneSegDirectorySynced(ctx, claim.AttemptID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAttempt(ctx, core.FinishRequest{AttemptID: claim.AttemptID, State: request.State, Reason: request.Reason,
		ByteCount: 376, Availability: core.AvailabilityFinal, Now: now.Add(4 * time.Second), Quality: main}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err := reopened.RecordingHistoryItem(ctx, r.Number)
	if err != nil || history == nil || history.Quality != main || !history.Playable() || history.FinalizationToken != request.Token {
		t.Fatalf("履歴quality=%+v err=%v", history, err)
	}
	completed, err := reopened.CompletedRecordings(ctx, 100, 0)
	if err != nil || len(completed) != 1 || completed[0].Quality != main {
		t.Fatalf("公開一覧=%+v err=%v", completed, err)
	}
}

func TestQualityMigrationPreservesOldRecordings(t *testing.T) {
	for _, result := range []struct {
		state     core.AttemptState
		reason    core.TerminalReason
		published bool
	}{
		{core.AttemptSucceeded, core.ReasonCompleted, true},
		{core.AttemptPartial, core.ReasonUserRequestedStop, true},
		{core.AttemptPartial, core.ReasonStreamTimeout, false},
	} {
		t.Run(string(result.reason), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			createDatabaseThroughMigration(t, root, 14)
			db, err := openWriter(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			legacy := &Store{writer: db, reader: db}
			claim, r, now := qualityRunningAttempt(t, legacy)
			var plannedState, plannedReason, token any
			if result.published {
				plannedState, plannedReason, token = result.state, result.reason, testID(t, 204).Bytes()
			}
			if result.reason == core.ReasonUserRequestedStop {
				if _, err := legacy.StopReservation(ctx, r.Number, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE recording_attempts SET state=?, terminal_reason=?, byte_count=376,
				actual_end_utc_ms=?, finalization_token=?, planned_final_state=?, planned_terminal_reason=? WHERE id=?`,
				result.state, result.reason, now.Add(2*time.Second).UnixMilli(), token, plannedState, plannedReason, claim.AttemptID.Bytes()); err != nil {
				t.Fatal(err)
			}
			state, availability := "PARTIAL", "PARTIAL"
			if result.published {
				state, availability = "FINALIZED", "FINAL"
			}
			if _, err := db.Exec(`UPDATE recording_segments SET state=?, availability=?, byte_count=376,
				file_synced=?, final_published=?, directory_synced=? WHERE attempt_id=?`, state, availability,
				result.published, result.published, result.published, claim.AttemptID.Bytes()); err != nil {
				t.Fatal(err)
			}
			before := legacyQualitySnapshot(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			migration, err := MigrateDatabaseWithBackup(ctx, root, MigrationRequest{AppliedAt: now.Add(3 * time.Second),
				BackupID: testID(t, 205), ProductVersion: "test", ProductCommit: strings.Repeat("a", 40), Now: func() time.Time { return now.Add(4 * time.Second) }})
			if err != nil || migration.Inspection.CurrentVersion != 15 || migration.Backup == nil || migration.Backup.SchemaVersion != 14 {
				t.Fatalf("migration=%+v err=%v", migration, err)
			}
			current, err := OpenStore(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			if after := legacyQualitySnapshot(t, current.reader); !reflect.DeepEqual(before, after) {
				t.Fatalf("旧録画が変化しました: before=%v after=%v", before, after)
			}
			history, err := current.RecordingHistoryItem(ctx, r.Number)
			if err != nil || history == nil || history.Quality != (core.QualitySummary{}) || history.Playable() != result.published {
				t.Fatalf("旧履歴の品質・公開条件=%+v err=%v", history, err)
			}
			var unknown, violations int
			if err := current.reader.QueryRow(`SELECT count(*) FROM recording_segments WHERE quality_status='UNKNOWN'
				AND cc_gap_events=0 AND fallback_events=0 AND reconnect_count=0 AND selection_unverified=0`).Scan(&unknown); err != nil || unknown != 2 {
				t.Fatalf("unknown=%d err=%v", unknown, err)
			}
			if err := current.reader.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
				t.Fatalf("FK=%d err=%v", violations, err)
			}
		})
	}
}

func legacyQualitySnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT a.state, COALESCE(a.terminal_reason,''), hex(a.finalization_token),
		COALESCE(a.planned_final_state,''), COALESCE(a.planned_terminal_reason,''), a.byte_count,
		s.ordinal, s.state, s.availability, s.relative_partial_path, s.relative_final_path,
		s.byte_count, s.file_synced, s.final_published, s.directory_synced, r.state, r.version
		FROM recording_attempts a JOIN recording_segments s ON s.attempt_id=a.id JOIN reservations r ON r.id=a.reservation_id ORDER BY a.id,s.ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var snapshot []string
	for rows.Next() {
		values := make([]any, 17)
		pointers := make([]any, 17)
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, fmt.Sprint(values))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestQualityMigrationRejectsSchemaFourteenDriftBeforeBackup(t *testing.T) {
	for _, mutation := range []string{
		`ALTER TABLE recording_segments ADD COLUMN unexpected INTEGER`,
		`DROP TRIGGER recording_attempts_stop_plan_update_check`,
	} {
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		createDatabaseThroughMigration(t, root, 14)
		db, err := openWriter(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(mutation); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		id := testID(t, 205)
		_, err = MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{AppliedAt: time.Unix(10, 0).UTC(),
			BackupID: id, ProductVersion: "test", ProductCommit: strings.Repeat("b", 40), Now: func() time.Time { return time.Unix(11, 0).UTC() }})
		if err == nil {
			t.Fatal("形状の異なる第14版を移行しました")
		}
		if _, err := FindBackupManifest(root, id); err == nil {
			t.Fatal("入口拒否より前にbackupを作りました")
		}
		inspection, err := InspectDatabase(context.Background(), root)
		if err != nil || inspection.CurrentVersion != 14 {
			t.Fatalf("inspection=%+v err=%v", inspection, err)
		}
	}
}

func TestQualitySQLConstraints(t *testing.T) {
	_, store := openMigratedStore(t)
	claim, _, _ := qualityRunningAttempt(t, store)
	for _, column := range []string{"cc_gap_events", "cc_duplicate_events", "tei_packets", "malformed_packet_events",
		"psi_continuity_events", "psi_crc_events", "psi_structure_events", "psi_limit_events", "sync_loss_events",
		"sync_recovered_events", "sync_discarded_bytes", "trailing_incomplete_bytes", "unfinished_psi_events", "fallback_events", "reconnect_count"} {
		upper := int64(2147483647)
		if column == "fallback_events" {
			upper = 1
		}
		if column == "reconnect_count" {
			upper = 3
		}
		for _, count := range []int64{-1, 0, upper, upper + 1} {
			_, err := store.writer.Exec(`UPDATE recording_segments SET `+column+`=? WHERE attempt_id=? AND ordinal=0`, count, claim.AttemptID.Bytes())
			if (err == nil) != (count == 0 || count == upper) {
				t.Fatalf("column=%s count=%d err=%v", column, count, err)
			}
		}
	}
	for _, column := range []string{"selection_unverified", "observation_limited", "quality_counters_saturated"} {
		for _, value := range []int{-1, 0, 1, 2} {
			_, err := store.writer.Exec(`UPDATE recording_segments SET `+column+`=? WHERE attempt_id=? AND ordinal=0`, value, claim.AttemptID.Bytes())
			if (err == nil) != (value == 0 || value == 1) {
				t.Fatalf("column=%s bool=%d err=%v", column, value, err)
			}
		}
	}
	for _, status := range []string{"UNKNOWN", "NO_ISSUES_OBSERVED", "DEGRADED", "SUCCESS"} {
		_, err := store.writer.Exec(`UPDATE recording_segments SET quality_status=? WHERE attempt_id=? AND ordinal=0`, status, claim.AttemptID.Bytes())
		if (err == nil) != (status != "SUCCESS") {
			t.Fatalf("status=%s err=%v", status, err)
		}
	}
}

func TestQualityLegacyTwelveSourceGateRemainsStrict(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	createDatabaseThroughMigration(t, root, 12)
	db, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	inspection := Inspection{State: StateBehind, CurrentVersion: 12, TargetVersion: 13}
	if err := validateMigrationSource(context.Background(), db, inspection); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE recording_segments ADD COLUMN unexpected INTEGER`); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationSource(context.Background(), db, inspection); err == nil {
		t.Fatal("第12版の形状照合を緩めました")
	}
}

func TestQualityCommunicationPlanRequiresUsefulActualTime(t *testing.T) {
	for _, duration := range []time.Duration{999 * time.Millisecond, time.Second} {
		_, store := openMigratedStore(t)
		claim, _, now := qualityRunningAttempt(t, store)
		_, err := store.BeginFinalization(context.Background(), core.FinalizeRequest{AttemptID: claim.AttemptID,
			Token: testID(t, 204), State: core.AttemptPartial, Reason: core.ReasonStreamEndedEarly,
			ByteCount: 188, Now: now.Add(duration), Quality: core.QualitySummary{Status: core.QualityDegraded},
			OneSeg: &core.OneSegResult{Availability: core.AvailabilityPartial, Reason: core.ReasonStreamUnavailable}})
		if (err == nil) != (duration == time.Second) {
			t.Fatalf("duration=%s err=%v", duration, err)
		}
	}
}

func TestQualityProgressDatabaseFailureRollsBack(t *testing.T) {
	_, store := openMigratedStore(t)
	claim, _, now := qualityRunningAttempt(t, store)
	if _, err := store.writer.Exec(`CREATE TRIGGER reject_quality BEFORE UPDATE OF quality_status ON recording_segments
		BEGIN SELECT RAISE(ABORT, 'synthetic quality persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := store.UpdateRecordingProgress(context.Background(), claim.AttemptID, 188, now.Add(time.Second),
		core.QualitySummary{Status: core.QualityDegraded, TEIPackets: 1})
	if err == nil {
		t.Fatal("品質のDB失敗を無視しました")
	}
	var count, heartbeat, segmentBytes int64
	var status string
	if err := store.reader.QueryRow(`SELECT a.byte_count,a.heartbeat_utc_ms,s.byte_count,s.quality_status
		FROM recording_attempts a JOIN recording_segments s ON s.attempt_id=a.id
		WHERE a.id=? AND s.ordinal=0`, claim.AttemptID.Bytes()).Scan(&count, &heartbeat, &segmentBytes, &status); err != nil {
		t.Fatal(err)
	}
	if count != 0 || segmentBytes != 0 || heartbeat != now.UnixMilli() || status != "UNKNOWN" {
		t.Fatal("失敗した進捗を部分的にcommitしました")
	}
}

func TestQualityCancelledWriterWaitDoesNotPlanFinalization(t *testing.T) {
	for _, reason := range []core.TerminalReason{core.ReasonCompleted, core.ReasonStreamEndedEarly, core.ReasonStreamTimeout, core.ReasonStreamReconnectExhausted} {
		t.Run(string(reason), func(t *testing.T) {
			_, store := openMigratedStore(t)
			claim, _, now := qualityRunningAttempt(t, store)
			held, err := store.writer.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state := core.AttemptPartial
			if reason == core.ReasonCompleted {
				state = core.AttemptSucceeded
			}
			request := core.FinalizeRequest{AttemptID: claim.AttemptID, Token: testID(t, 204),
				State: state, Reason: reason, ByteCount: 188, Now: now.Add(time.Second),
				OneSeg: &core.OneSegResult{Availability: core.AvailabilityPartial, Reason: core.ReasonStreamUnavailable}}
			before := store.writer.Stats().WaitCount
			done := make(chan error, 1)
			go func() {
				_, err := store.BeginFinalization(ctx, request)
				done <- err
			}()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			for store.writer.Stats().WaitCount == before {
				select {
				case <-ticker.C:
				case <-deadline.C:
					t.Fatal("確定処理がwriter待ちへ入りませんでした")
				}
			}
			var persisted string
			if err := store.reader.QueryRow(`SELECT state FROM recording_attempts WHERE id=?`, claim.AttemptID.Bytes()).Scan(&persisted); err != nil || persisted != "RECORDING" {
				t.Fatalf("取消し前state=%s err=%v", persisted, err)
			}
			cancel()
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, core.ErrFinalizationUnavailable) {
					t.Fatalf("取消し後の確定が拒否されませんでした: %v", err)
				}
			case <-deadline.C:
				t.Fatal("取消し後の確定処理が終了しませんでした")
			}
			var noToken bool
			if err := store.reader.QueryRow(`SELECT state,finalization_token IS NULL FROM recording_attempts WHERE id=?`, claim.AttemptID.Bytes()).Scan(&persisted, &noToken); err != nil || persisted != "RECORDING" || !noToken {
				t.Fatalf("取消された確定計画を保存しました: state=%s no_token=%t err=%v", persisted, noToken, err)
			}
		})
	}
}
