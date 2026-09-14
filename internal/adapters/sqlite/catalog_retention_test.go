package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestCatalogRetentionMigratesSchema13To14(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createDatabaseThroughMigration(t, root, 13)

	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(100).UTC(), BackupID: testID(t, 240), ProductVersion: "test",
		ProductCommit: strings.Repeat("a", 40), Now: func() time.Time { return time.UnixMilli(101).UTC() },
	})
	if err != nil || result.Inspection.State != StateCurrent || result.Inspection.CurrentVersion != 14 || result.Backup == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var deleteTrigger, updateTrigger int
	if err := store.reader.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name='program_revisions_no_delete'`).Scan(&deleteTrigger); err != nil {
		t.Fatal(err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name='program_revisions_no_update'`).Scan(&updateTrigger); err != nil {
		t.Fatal(err)
	}
	if deleteTrigger != 0 || updateTrigger != 1 {
		t.Fatalf("revision triggers delete=%d update=%d", deleteTrigger, updateTrigger)
	}

	for _, index := range []string{
		"catalog_gc_sync_terminal_idx",
		"catalog_gc_program_instance_cutoff_idx",
		"catalog_gc_service_cutoff_idx",
		"catalog_gc_program_observation_revision_idx",
		"catalog_gc_program_observation_instance_idx",
		"catalog_gc_service_observation_service_idx",
		"catalog_gc_reservation_revision_idx",
		"catalog_gc_reservation_instance_idx",
	} {
		var count int
		if err := store.reader.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("index %s count=%d", index, count)
		}
	}
}

func TestPruneCatalogBatchDeletesProgramObservationsBeforeOtherStages(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID, syncID := testID(t, 241), testID(t, 242)
	serviceID, instanceID, revisionID := testID(t, 243), testID(t, 244), testID(t, 245)
	insertRetentionFixture(t, store, backendID, syncID, serviceID, instanceID, revisionID)

	result, err := store.PruneCatalogBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var programs, services int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_observations`).Scan(&programs); err != nil {
		t.Fatal(err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM service_observations`).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if programs != 0 || services != 1 {
		t.Fatalf("observations programs=%d services=%d", programs, services)
	}
}

func insertRetentionFixture(t *testing.T, store *Store, backendID, syncID, serviceID, instanceID, revisionID catalogmodel.ID) {
	t.Helper()
	hash := sha256.Sum256([]byte(syncID.String()))
	contentHash := sha256.Sum256([]byte(revisionID.String()))
	tx, err := store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 1, 1)`, backendID.Bytes(), hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO catalog_syncs
		(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms,
		 service_count, program_count, correlation_id, failure_reason)
		VALUES (?, ?, 'FAILED', 1, 2, 1, 1, 'retention-fixture', 'failed')`, syncID.Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO services
		(id, backend_instance_id, provider_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, 'service', 'VERIFIED', 1, 1)`, serviceID.Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO service_observations
		(sequence, sync_id, service_id, provider_locator, display_name, validation_state, observation_hash)
		VALUES (1, ?, ?, 'service', 'service', 'VALID', ?)`, syncID.Bytes(), serviceID.Bytes(), hash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO program_instances
		(id, service_id, provider_event_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, 'event', 'VERIFIED', 1, 1)`, instanceID.Bytes(), serviceID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO program_revisions
		(id, program_instance_id, revision_number, content_hash, validation_state, created_at_utc_ms)
		VALUES (?, ?, 1, ?, 'VALID', 1)`, revisionID.Bytes(), instanceID.Bytes(), contentHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO program_observations
		(sequence, sync_id, provider_service_locator, provider_event_locator, content_hash,
		 program_instance_id, program_revision_id, classification)
		VALUES (2, ?, 'service', 'event', ?, ?, ?, 'NEW_INSTANCE')`, syncID.Bytes(), contentHash[:], instanceID.Bytes(), revisionID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRetentionMigrationReadbackChecksumAndForeignKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createDatabaseThroughMigration(t, root, 13)
	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(100).UTC(), BackupID: testID(t, 246), ProductVersion: "test",
		ProductCommit: strings.Repeat("b", 40), Now: func() time.Time { return time.UnixMilli(101).UTC() },
	})
	if err != nil || result.Inspection.CurrentVersion != 14 || result.Backup == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var checksum []byte
	if err := store.reader.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=14`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checksum, migrations[13].checksum[:]) {
		t.Fatalf("schema 14 checksum=%x want=%x", checksum, migrations[13].checksum)
	}
	var violations int
	if err := store.reader.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
}

func TestPruneCatalogBatchRunsOneStageAtATime(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID, syncID := retentionID(300), retentionID(301)
	serviceID, instanceID, revisionID := retentionID(302), retentionID(303), retentionID(304)
	insertRetentionBackend(t, store, backendID)
	insertRetentionSync(t, store, backendID, syncID, "FAILED", 1, 2)
	insertRetentionService(t, store, serviceID, backendID, "stage-service", 1)
	insertRetentionInstance(t, store, instanceID, serviceID, "stage-event", 1)
	insertRetentionRevision(t, store, revisionID, instanceID, 1, 1)
	insertRetentionServiceObservation(t, store, 11, syncID, serviceID)
	insertRetentionProgramObservation(t, store, 12, syncID, "stage-service", "stage-event", instanceID, revisionID, "NEW_INSTANCE")

	ctx := context.Background()
	result, err := store.PruneCatalogBatch(ctx, 100)
	if err != nil || result.ProgramObservationsDeleted != 1 || result.ServiceObservationsDeleted != 0 || result.AllEmpty {
		t.Fatalf("program stage result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(ctx, 100)
	if err != nil || result.ProgramObservationsDeleted != 0 || result.ServiceObservationsDeleted != 1 || result.AllEmpty {
		t.Fatalf("service stage result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(ctx, 100)
	if err != nil || result.CatalogSyncsDeleted != 1 || result.AllEmpty {
		t.Fatalf("sync stage result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(ctx, 100)
	if err != nil || result.ProgramRevisionsDeleted != 1 || result.AllEmpty {
		t.Fatalf("revision stage result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(ctx, 100)
	if err != nil || result.ProgramInstancesDeleted != 1 || result.AllEmpty {
		t.Fatalf("instance stage result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(ctx, 100)
	if err != nil || result.ServicesDeleted != 1 || result.AllEmpty {
		t.Fatalf("service stage result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(ctx, 100)
	if err != nil || !result.AllEmpty || result.ProgramObservationsDeleted != 0 || result.ServicesDeleted != 0 {
		t.Fatalf("empty result=%+v err=%v", result, err)
	}
	var backends, violations int
	if err := store.reader.QueryRow(`SELECT count(*) FROM backend_instances`).Scan(&backends); err != nil || backends != 1 {
		t.Fatalf("backend count=%d err=%v", backends, err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
}

func TestPruneCatalogBatchPreservesLatestThreeRunningAndBackendIsolation(t *testing.T) {
	_, store := openMigratedStore(t)
	backendA, backendB := retentionID(320), retentionID(321)
	insertRetentionBackend(t, store, backendA)
	insertRetentionBackend(t, store, backendB)
	sequence := int64(100)
	for index, finished := range []int64{10, 20, 30, 40, 50} {
		syncID := retentionID(uint64(322 + index))
		insertRetentionSync(t, store, backendA, syncID, "COMPLETED", finished-1, finished)
		insertRetentionProgramObservation(t, store, sequence, syncID, "a", fmt.Sprintf("a-%d", index), catalogmodel.ID{}, catalogmodel.ID{}, "INVALID")
		sequence++
	}
	runningID := retentionID(327)
	insertRetentionSync(t, store, backendA, runningID, "RUNNING", 60, 0)
	insertRetentionProgramObservation(t, store, sequence, runningID, "a", "a-running", catalogmodel.ID{}, catalogmodel.ID{}, "INVALID")
	sequence++
	for index, finished := range []int64{10, 20, 30, 40} {
		syncID := retentionID(uint64(328 + index))
		insertRetentionSync(t, store, backendB, syncID, "COMPLETED", finished-1, finished)
		insertRetentionProgramObservation(t, store, sequence, syncID, "b", fmt.Sprintf("b-%d", index), catalogmodel.ID{}, catalogmodel.ID{}, "INVALID")
		sequence++
	}
	pruneCatalogUntilEmpty(t, store, 100)

	var syncs, observations int
	if err := store.reader.QueryRow(`SELECT count(*) FROM catalog_syncs`).Scan(&syncs); err != nil || syncs != 7 {
		t.Fatalf("sync count=%d err=%v", syncs, err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_observations`).Scan(&observations); err != nil || observations != 7 {
		t.Fatalf("observation count=%d err=%v", observations, err)
	}
	var running int
	if err := store.reader.QueryRow(`SELECT count(*) FROM catalog_syncs WHERE state='RUNNING'`).Scan(&running); err != nil || running != 1 {
		t.Fatalf("running count=%d err=%v", running, err)
	}
	var backendAOld, backendBOld int
	if err := store.reader.QueryRow(`SELECT count(*) FROM catalog_syncs WHERE backend_instance_id=? AND finished_at_utc_ms<30`, backendA.Bytes()).Scan(&backendAOld); err != nil || backendAOld != 0 {
		t.Fatalf("backend A old syncs=%d err=%v", backendAOld, err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM catalog_syncs WHERE backend_instance_id=? AND finished_at_utc_ms<20`, backendB.Bytes()).Scan(&backendBOld); err != nil || backendBOld != 0 {
		t.Fatalf("backend B old syncs=%d err=%v", backendBOld, err)
	}
}

func TestPruneCatalogBatchPreservesLatestThreeForEveryGenerationCount(t *testing.T) {
	_, store := openMigratedStore(t)
	counts := []int{0, 1, 2, 3, 4}
	sequence := int64(200)
	for backendIndex, generationCount := range counts {
		backendID := retentionID(uint64(500 + backendIndex))
		insertRetentionBackend(t, store, backendID)
		for generation := 0; generation < generationCount; generation++ {
			syncID := retentionID(uint64(510 + backendIndex*10 + generation))
			finished := int64(generation + 1)
			insertRetentionSync(t, store, backendID, syncID, "COMPLETED", finished-1, finished)
			insertRetentionProgramObservation(t, store, sequence, syncID, "generation-service", fmt.Sprintf("generation-event-%d-%d", backendIndex, generation), catalogmodel.ID{}, catalogmodel.ID{}, "INVALID")
			sequence++
		}
	}
	pruneCatalogUntilEmpty(t, store, 100)

	for backendIndex, generationCount := range counts {
		want := generationCount
		if want > 3 {
			want = 3
		}
		var got int
		if err := store.reader.QueryRow(`SELECT count(*) FROM catalog_syncs WHERE backend_instance_id=?`, retentionID(uint64(500+backendIndex)).Bytes()).Scan(&got); err != nil || got != want {
			t.Fatalf("backend %d retained generations=%d want=%d err=%v", backendIndex, got, want, err)
		}
	}
}

func TestPruneCatalogBatchUsesStrictCutoffForSummariesInstancesAndServices(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := retentionID(340)
	insertRetentionBackend(t, store, backendID)
	for index, finished := range []int64{99, 100, 101} {
		insertRetentionSync(t, store, backendID, retentionID(uint64(341+index)), "FAILED", 1, finished)
	}
	serviceID := retentionID(350)
	insertRetentionService(t, store, serviceID, backendID, "boundary-service", 101)
	for index, seen := range []int64{99, 100, 101} {
		insertRetentionInstance(t, store, retentionID(uint64(351+index)), serviceID, fmt.Sprintf("boundary-event-%d", index), seen)
	}
	for index, seen := range []int64{99, 100, 101} {
		insertRetentionService(t, store, retentionID(uint64(361+index)), backendID, fmt.Sprintf("boundary-service-%d", index), seen)
	}
	pruneCatalogUntilEmpty(t, store, 100)

	var summaries, instances, services int
	if err := store.reader.QueryRow(`SELECT count(*) FROM catalog_syncs`).Scan(&summaries); err != nil || summaries != 2 {
		t.Fatalf("summary count=%d err=%v", summaries, err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_instances`).Scan(&instances); err != nil || instances != 2 {
		t.Fatalf("instance count=%d err=%v", instances, err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM services`).Scan(&services); err != nil || services != 3 {
		t.Fatalf("service count=%d err=%v", services, err)
	}
}

func TestPruneCatalogBatchProtectsObservationReservationAndAutomaticMatchReferences(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := retentionID(380)
	insertRetentionBackend(t, store, backendID)
	protectedSyncID := retentionID(381)
	insertRetentionSync(t, store, backendID, protectedSyncID, "COMPLETED", 1, 2)
	serviceID := retentionID(382)
	insertRetentionService(t, store, serviceID, backendID, "reference-service", 1)
	protectedObservationInstance := retentionID(383)
	protectedObservationRevision := retentionID(384)
	insertRetentionInstance(t, store, protectedObservationInstance, serviceID, "observation-event", 1)
	insertRetentionRevision(t, store, protectedObservationRevision, protectedObservationInstance, 1, 1)
	insertRetentionProgramObservation(t, store, 381, protectedSyncID, "reference-service", "observation-event", protectedObservationInstance, protectedObservationRevision, "NEW_INSTANCE")

	reservationInstance := retentionID(385)
	reservationRevision := retentionID(386)
	insertRetentionInstance(t, store, reservationInstance, serviceID, "reservation-event", 1)
	insertRetentionRevision(t, store, reservationRevision, reservationInstance, 1, 1)
	insertRetentionReservation(t, store, retentionID(387), backendID, reservationInstance, reservationRevision, 10)

	automaticInstance := retentionID(388)
	automaticRevision := retentionID(389)
	insertRetentionInstance(t, store, automaticInstance, serviceID, "automatic-event", 1)
	insertRetentionRevision(t, store, automaticRevision, automaticInstance, 1, 1)
	automaticReservationID := retentionID(390)
	insertRetentionReservation(t, store, automaticReservationID, backendID, automaticInstance, automaticRevision, 11)
	ruleID := retentionID(391)
	if _, err := store.writer.Exec(`INSERT INTO automatic_reservation_rules
		(id, version, search_json, recording_json, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, 1, '{}', '{}', 1, 1)`, ruleID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO automatic_reservation_matches
		(rule_id, program_instance_id, reservation_id, created_at_utc_ms)
		VALUES (?, ?, ?, 1)`, ruleID.Bytes(), automaticInstance.Bytes(), automaticReservationID.Bytes()); err != nil {
		t.Fatal(err)
	}

	unreferencedInstance := retentionID(392)
	unreferencedRevision := retentionID(393)
	insertRetentionInstance(t, store, unreferencedInstance, serviceID, "unreferenced-event", 1)
	insertRetentionRevision(t, store, unreferencedRevision, unreferencedInstance, 1, 1)
	pruneCatalogUntilEmpty(t, store, 100)

	for _, id := range []catalogmodel.ID{protectedObservationInstance, reservationInstance, automaticInstance} {
		var count int
		if err := store.reader.QueryRow(`SELECT count(*) FROM program_instances WHERE id=?`, id.Bytes()).Scan(&count); err != nil || count != 1 {
			t.Fatalf("protected instance=%s count=%d err=%v", id, count, err)
		}
	}
	var count int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_instances WHERE id=?`, unreferencedInstance.Bytes()).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unreferenced instance count=%d err=%v", count, err)
	}
}

func TestPruneCatalogBatchLimitsEachTransactionToOneThousandRows(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID, syncID := retentionID(400), retentionID(401)
	insertRetentionBackend(t, store, backendID)
	insertRetentionSync(t, store, backendID, syncID, "FAILED", 1, 1001)
	tx, err := store.writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := int64(1); sequence <= 1001; sequence++ {
		if _, err := tx.Exec(`INSERT INTO program_observations
			(sequence, sync_id, provider_service_locator, provider_event_locator, classification)
			VALUES (?, ?, 'batch-service', ?, 'INVALID')`, sequence, syncID.Bytes(), fmt.Sprintf("event-%04d", sequence)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := store.PruneCatalogBatch(context.Background(), 100)
	if err != nil || result.ProgramObservationsDeleted != 1000 || result.AllEmpty {
		t.Fatalf("first batch result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(context.Background(), 100)
	if err != nil || result.ProgramObservationsDeleted != 1 || result.AllEmpty {
		t.Fatalf("second batch result=%+v err=%v", result, err)
	}
	result, err = store.PruneCatalogBatch(context.Background(), 100)
	if err != nil || !result.AllEmpty {
		t.Fatalf("empty batch result=%+v err=%v", result, err)
	}
}

func TestPruneCatalogBatchRollsBackOnFailureAndConvergesAfterRetry(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID, syncID := retentionID(420), retentionID(421)
	serviceID, instanceID := retentionID(422), retentionID(423)
	insertRetentionBackend(t, store, backendID)
	insertRetentionSync(t, store, backendID, syncID, "FAILED", 1, 2)
	insertRetentionService(t, store, serviceID, backendID, "retry-service", 1)
	insertRetentionInstance(t, store, instanceID, serviceID, "retry-event", 1)
	insertRetentionServiceObservation(t, store, 421, syncID, serviceID)
	if _, err := store.writer.Exec(`CREATE TRIGGER retention_test_abort_service
		BEFORE DELETE ON service_observations
		BEGIN SELECT RAISE(ABORT, 'retry'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneCatalogBatch(context.Background(), 100); err == nil {
		t.Fatal("failure trigger後のGCが成功しました")
	}
	var programs, services int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_observations`).Scan(&programs); err != nil || programs != 0 {
		t.Fatalf("rolled back program observations=%d err=%v", programs, err)
	}
	if err := store.reader.QueryRow(`SELECT count(*) FROM service_observations`).Scan(&services); err != nil || services != 1 {
		t.Fatalf("rolled back service observations=%d err=%v", services, err)
	}
	if _, err := store.writer.Exec(`DROP TRIGGER retention_test_abort_service`); err != nil {
		t.Fatal(err)
	}
	pruneCatalogUntilEmpty(t, store, 100)
	result, err := store.PruneCatalogBatch(context.Background(), 100)
	if err != nil || !result.AllEmpty {
		t.Fatalf("idempotent result=%+v err=%v", result, err)
	}
}

func TestPruneCatalogBatchCancellationLeavesRowsForRetry(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID, syncID := retentionID(430), retentionID(431)
	insertRetentionBackend(t, store, backendID)
	insertRetentionSync(t, store, backendID, syncID, "FAILED", 1, 1)
	insertRetentionProgramObservation(t, store, 431, syncID, "cancel-service", "cancel-event", catalogmodel.ID{}, catalogmodel.ID{}, "INVALID")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.PruneCatalogBatch(ctx, 100); err == nil {
		t.Fatal("cancel済みcontextのGCが成功しました")
	}
	var count int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_observations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancel後のobservations=%d err=%v", count, err)
	}
	result, err := store.PruneCatalogBatch(context.Background(), 100)
	if err != nil || result.ProgramObservationsDeleted != 1 {
		t.Fatalf("retry result=%+v err=%v", result, err)
	}
}

func TestPruneCatalogBatchResetsExpiredProviderIdentity(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID, syncID := retentionID(440), retentionID(441)
	serviceID, instanceID, revisionID := retentionID(442), retentionID(443), retentionID(444)
	insertRetentionBackend(t, store, backendID)
	insertRetentionSync(t, store, backendID, syncID, "FAILED", 1, 2)
	insertRetentionService(t, store, serviceID, backendID, "identity-service", 1)
	insertRetentionInstance(t, store, instanceID, serviceID, "identity-event", 1)
	insertRetentionRevision(t, store, revisionID, instanceID, 1, 1)
	pruneCatalogUntilEmpty(t, store, 100)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: retentionID(445), BackendID: backendID, StartedAtMS: 200, CorrelationID: "identity-reset",
	}); err != nil {
		t.Fatal(err)
	}
	serviceNumber, transportID, serviceNumberID := int64(1), int64(2), int64(3)
	if err := store.StoreServices(context.Background(), retentionID(445), []catalogmodel.ServiceObservation{{
		ProviderLocator: "identity-service", NetworkID: &serviceNumber, TransportID: &transportID, ServiceID: &serviceNumberID,
		DisplayName: "service", Validation: catalogmodel.ValidationValid,
	}}); err != nil {
		t.Fatal(err)
	}
	start, duration, eventID, title := int64(1000), int64(60000), int64(4), "program"
	if err := store.StorePrograms(context.Background(), retentionID(445), true, []catalogmodel.ProgramObservation{{
		ServiceLocator: "identity-service", EventLocator: "identity-event", RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &start, DurationMS: &duration, Title: &title, Validation: catalogmodel.ValidationValid},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), retentionID(445), 201, 1, 1); err != nil {
		t.Fatal(err)
	}
	var newInstance []byte
	var revisionNumber int
	var classification string
	if err := store.reader.QueryRow(`SELECT pi.id, pr.revision_number, po.classification
		FROM program_observations po
		JOIN program_instances pi ON pi.id=po.program_instance_id
		JOIN program_revisions pr ON pr.id=po.program_revision_id
		WHERE po.sync_id=?`, retentionID(445).Bytes()).Scan(&newInstance, &revisionNumber, &classification); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(newInstance, instanceID.Bytes()) || revisionNumber != 1 || classification != "NEW_INSTANCE" {
		t.Fatalf("identity reset id=%x revision=%d classification=%s", newInstance, revisionNumber, classification)
	}
}

func TestPruneCatalogBatchCandidateQueryPlansUseRetentionIndexes(t *testing.T) {
	_, store := openMigratedStore(t)
	tests := []struct {
		name    string
		query   string
		args    []any
		indexes []string
		tables  []string
	}{
		{name: "program observations", query: pruneProgramObservationsSQL,
			indexes: []string{"catalog_gc_sync_terminal_idx", "program_observations_sync_instance_idx", "catalog_syncs_completed_backend_idx"},
			tables:  []string{"catalog_syncs", "program_observations"}},
		{name: "service observations", query: pruneServiceObservationsSQL,
			indexes: []string{"catalog_gc_sync_terminal_idx", "service_observations_sync_service_idx", "catalog_syncs_completed_backend_idx"},
			tables:  []string{"catalog_syncs", "service_observations"}},
		{name: "catalog syncs", query: pruneCatalogSyncsSQL, args: []any{100},
			indexes: []string{"catalog_gc_sync_terminal_idx", "service_observations_sync_service_idx", "program_observations_sync_instance_idx", "catalog_syncs_completed_backend_idx"},
			tables:  []string{"catalog_syncs", "service_observations", "program_observations"}},
		{name: "program revisions", query: pruneProgramRevisionsSQL, args: []any{100},
			indexes: []string{"catalog_gc_program_instance_cutoff_idx", "catalog_gc_program_observation_instance_idx", "catalog_gc_program_observation_revision_idx", "catalog_gc_reservation_instance_idx", "catalog_gc_reservation_revision_idx", "program_revisions_instance_number_idx"},
			tables:  []string{"program_instances", "program_revisions", "program_observations", "reservations", "automatic_reservation_matches"}},
		{name: "program instances", query: pruneProgramInstancesSQL, args: []any{100},
			indexes: []string{"catalog_gc_program_instance_cutoff_idx", "program_revisions_instance_number_idx", "catalog_gc_program_observation_instance_idx", "catalog_gc_reservation_instance_idx"},
			tables:  []string{"program_instances", "program_revisions", "program_observations", "reservations", "automatic_reservation_matches"}},
		{name: "services", query: pruneServicesSQL, args: []any{100},
			indexes: []string{"catalog_gc_service_cutoff_idx", "catalog_gc_service_observation_service_idx", "program_instances_service_event_idx"},
			tables:  []string{"services", "service_observations", "program_instances"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertQueryPlanUsesAny(t, store.reader, test.indexes, test.query, test.args...)
			assertQueryPlanHasNoTableScan(t, store.reader, test.tables, test.query, test.args...)
		})
	}
}

func retentionID(seed uint64) catalogmodel.ID {
	var id catalogmodel.ID
	binary.BigEndian.PutUint64(id[:8], seed)
	binary.BigEndian.PutUint64(id[8:], seed^0x9e3779b97f4a7c15)
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

func insertRetentionBackend(t *testing.T, store *Store, id catalogmodel.ID) {
	t.Helper()
	hash := sha256.Sum256([]byte("retention-backend:" + id.String()))
	if _, err := store.writer.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 1, 1)`, id.Bytes(), hash[:]); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionSync(t *testing.T, store *Store, backendID, syncID catalogmodel.ID, state string, started, finished int64) {
	t.Helper()
	correlation := "retention-sync-" + syncID.String()
	switch state {
	case "COMPLETED":
		if _, err := store.writer.Exec(`INSERT INTO catalog_syncs
			(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, program_count, correlation_id)
			VALUES (?, ?, 'COMPLETED', ?, ?, 1, ?)`, syncID.Bytes(), backendID.Bytes(), started, finished, correlation); err != nil {
			t.Fatal(err)
		}
	case "FAILED":
		if _, err := store.writer.Exec(`INSERT INTO catalog_syncs
			(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, program_count, failure_reason, correlation_id)
			VALUES (?, ?, 'FAILED', ?, ?, 1, 'failed', ?)`, syncID.Bytes(), backendID.Bytes(), started, finished, correlation); err != nil {
			t.Fatal(err)
		}
	case "RUNNING":
		if _, err := store.writer.Exec(`INSERT INTO catalog_syncs
			(id, backend_instance_id, state, started_at_utc_ms, correlation_id)
			VALUES (?, ?, 'RUNNING', ?, ?)`, syncID.Bytes(), backendID.Bytes(), started, correlation); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown sync state %s", state)
	}
}

func insertRetentionService(t *testing.T, store *Store, id, backendID catalogmodel.ID, locator string, lastSeen int64) {
	t.Helper()
	if _, err := store.writer.Exec(`INSERT INTO services
		(id, backend_instance_id, provider_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, ?, 'VERIFIED', 1, ?)`, id.Bytes(), backendID.Bytes(), locator, lastSeen); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionInstance(t *testing.T, store *Store, id, serviceID catalogmodel.ID, locator string, lastSeen int64) {
	t.Helper()
	if _, err := store.writer.Exec(`INSERT INTO program_instances
		(id, service_id, provider_event_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, ?, 'VERIFIED', 1, ?)`, id.Bytes(), serviceID.Bytes(), locator, lastSeen); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionRevision(t *testing.T, store *Store, id, instanceID catalogmodel.ID, number int, created int64) {
	t.Helper()
	hash := sha256.Sum256([]byte(fmt.Sprintf("retention-revision:%s:%d", id, number)))
	if _, err := store.writer.Exec(`INSERT INTO program_revisions
		(id, program_instance_id, revision_number, content_hash, title, validation_state, created_at_utc_ms)
		VALUES (?, ?, ?, ?, 'program', 'VALID', ?)`, id.Bytes(), instanceID.Bytes(), number, hash[:], created); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionServiceObservation(t *testing.T, store *Store, sequence int64, syncID, serviceID catalogmodel.ID) {
	t.Helper()
	hash := sha256.Sum256([]byte(fmt.Sprintf("retention-service-observation:%d", sequence)))
	if _, err := store.writer.Exec(`INSERT INTO service_observations
		(sequence, sync_id, service_id, provider_locator, display_name, validation_state, observation_hash)
		VALUES (?, ?, ?, 'service', 'service', 'VALID', ?)`, sequence, syncID.Bytes(), serviceID.Bytes(), hash[:]); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionProgramObservation(t *testing.T, store *Store, sequence int64, syncID catalogmodel.ID, serviceLocator, eventLocator string, instanceID, revisionID catalogmodel.ID, classification string) {
	t.Helper()
	if instanceID == (catalogmodel.ID{}) {
		if _, err := store.writer.Exec(`INSERT INTO program_observations
			(sequence, sync_id, provider_service_locator, provider_event_locator, classification)
			VALUES (?, ?, ?, ?, ?)`, sequence, syncID.Bytes(), serviceLocator, eventLocator, classification); err != nil {
			t.Fatal(err)
		}
		return
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("retention-program-observation:%d", sequence)))
	if _, err := store.writer.Exec(`INSERT INTO program_observations
		(sequence, sync_id, provider_service_locator, provider_event_locator, content_hash,
		 program_instance_id, program_revision_id, classification)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sequence, syncID.Bytes(), serviceLocator, eventLocator, hash[:], instanceID.Bytes(), revisionID.Bytes(), classification); err != nil {
		t.Fatal(err)
	}
}

func insertRetentionReservation(t *testing.T, store *Store, reservationID, backendID, instanceID, revisionID catalogmodel.ID, seed int) {
	t.Helper()
	if _, err := store.writer.Exec(`INSERT INTO reservations(
		id, version, state, program_instance_id, program_revision_id, backend_instance_id,
		provider_service_locator, tuning_target, network_id, transport_stream_id, service_id, event_id,
		title, station_name, start_at_utc_ms, duration_seconds, requested_priority, requested_follow,
		effective_follow, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, 1, 'ACTIVE', ?, ?, ?, 'service', 'target', ?, ?, ?, ?,
			'program', 'station', 100000, 60, 3, 0, 0, 1, 1)`, reservationID.Bytes(), instanceID.Bytes(), revisionID.Bytes(), backendID.Bytes(), seed, seed, seed, seed); err != nil {
		t.Fatal(err)
	}
}

func pruneCatalogUntilEmpty(t *testing.T, store *Store, cutoff int64) {
	t.Helper()
	for attempt := 0; attempt < 128; attempt++ {
		result, err := store.PruneCatalogBatch(context.Background(), cutoff)
		if err != nil {
			t.Fatalf("GC attempt=%d err=%v", attempt, err)
		}
		if result.AllEmpty {
			return
		}
	}
	t.Fatal("GC did not converge")
}
