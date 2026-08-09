package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func openMigratedStore(t *testing.T) (string, *Store) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	writer, err := openWriter(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := inspect(ctx, writer)
	if err != nil || before.State != StateEmpty {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	after, err := migrate(ctx, writer, 1785628800000)
	if err != nil || after.State != StateCurrent || after.CurrentVersion != 9 {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return root, store
}

func TestMigrateFreshDatabaseAndOpenPools(t *testing.T) {
	root, store := openMigratedStore(t)
	if got := store.writer.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("writer max=%d", got)
	}
	if got := store.reader.Stats().MaxOpenConnections; got != 4 {
		t.Fatalf("reader max=%d", got)
	}
	if _, err := store.reader.Exec(`CREATE TABLE forbidden(value INTEGER)`); err == nil {
		t.Fatal("readerからのwriteが成功しました")
	}
	var tableCount int
	if err := store.reader.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 14 {
		t.Fatalf("table count=%d, want=14", tableCount)
	}
	info, err := os.Stat(filepath.Join(root, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode=%o", info.Mode().Perm())
	}
}

func TestRestrictDatabaseSidecarsNarrowsExistingModes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, databaseFilename)
	if err := os.WriteFile(database, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(database+suffix, []byte("sidecar"), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	if err := restrictDatabaseSidecars(database); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, err := os.Lstat(database + suffix)
		if err != nil || !ownerOnlyRegular(info) {
			t.Fatalf("sidecar=%s mode=%v err=%v", suffix, fileMode(info), err)
		}
	}
}

func TestRestrictDatabaseSidecarsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, databaseFilename)
	if err := os.WriteFile(database, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(database, database+"-wal"); err != nil {
		t.Fatal(err)
	}
	if err := restrictDatabaseSidecars(database); err == nil {
		t.Fatal("WALのsymlinkを受理しました")
	}
}

func fileMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode()
}

func TestMigrateVersionOneWithVerifiedBackup(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createVersionOneDatabase(t, root, true)
	before, err := InspectDatabase(context.Background(), root)
	if err != nil || before.State != StateBehind || before.CurrentVersion != 1 || before.TargetVersion != 9 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(1785628800000).UTC(), BackupID: testID(t, 90), ProductVersion: "test",
		ProductCommit: strings.Repeat("a", 40), Now: func() time.Time { return time.UnixMilli(1785628800001).UTC() },
	})
	if err != nil || result.Inspection.State != StateCurrent || result.Inspection.CurrentVersion != 9 || result.Backup == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Backup.Purpose != "pre_migration" || result.Backup.SchemaVersion != 1 ||
		result.Backup.MigrationFromSchema == nil || *result.Backup.MigrationFromSchema != 1 ||
		result.Backup.MigrationToSchema == nil || *result.Backup.MigrationToSchema != 9 {
		t.Fatalf("backup=%+v", result.Backup)
	}
	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var backendKind, syncState string
	var programCount int
	if err := store.reader.QueryRow(`SELECT provider_kind FROM backend_instances`).Scan(&backendKind); err != nil {
		t.Fatal(err)
	}
	if err := store.reader.QueryRow(`SELECT state, program_count FROM catalog_syncs`).Scan(&syncState, &programCount); err != nil {
		t.Fatal(err)
	}
	if backendKind != "FAKE" || syncState != "COMPLETED" || programCount != 100_000 {
		t.Fatalf("kind=%s state=%s programs=%d", backendKind, syncState, programCount)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := FindBackupManifest(root, testID(t, 90))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreOffline(context.Background(), root, RestoreRequest{
		OperationID: testID(t, 91), BackupManifest: manifest,
		CreatedAt: time.UnixMilli(1785628800002).UTC(), Now: func() time.Time { return time.UnixMilli(1785628800003).UTC() },
	})
	if err != nil || restored.Phase != RestoreCommitted {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	afterRestore, err := InspectDatabase(context.Background(), root)
	if err != nil || afterRestore.State != StateBehind || afterRestore.CurrentVersion != 1 {
		t.Fatalf("after restore=%+v err=%v", afterRestore, err)
	}
}

func TestMigrateVersionTwoWithVerifiedBackup(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createVersionTwoDatabase(t, root)
	before, err := InspectDatabase(context.Background(), root)
	if err != nil || before.State != StateBehind || before.CurrentVersion != 2 || before.TargetVersion != 9 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(1785628800000).UTC(), BackupID: testID(t, 86), ProductVersion: "test",
		ProductCommit: strings.Repeat("c", 40), Now: func() time.Time { return time.UnixMilli(1785628800001).UTC() },
	})
	if err != nil || result.Inspection.State != StateCurrent || result.Inspection.CurrentVersion != 9 || result.Backup == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Backup.SchemaVersion != 2 || result.Backup.MigrationFromSchema == nil ||
		*result.Backup.MigrationFromSchema != 2 || result.Backup.MigrationToSchema == nil ||
		*result.Backup.MigrationToSchema != 9 {
		t.Fatalf("backup=%+v", result.Backup)
	}
	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var providerKind, correlationID string
	if err := store.reader.QueryRow(`SELECT provider_kind FROM backend_instances WHERE id=?`, testID(t, 84).Bytes()).Scan(&providerKind); err != nil {
		t.Fatal(err)
	}
	if err := store.reader.QueryRow(`SELECT correlation_id FROM catalog_syncs WHERE id=?`, testID(t, 85).Bytes()).Scan(&correlationID); err != nil {
		t.Fatal(err)
	}
	if providerKind != "FAKE" || correlationID != "version-two" {
		t.Fatalf("provider=%s correlation=%s", providerKind, correlationID)
	}
	for _, table := range []string{"reservations", "ctrlcmd_reservation_ids", "recording_attempts", "recording_segments"} {
		var count int
		if err := store.reader.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table=%s count=%d err=%v", table, count, err)
		}
	}
}

func TestMigrateVersionThreeAddsReservationTerminalReason(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createVersionThreeDatabase(t, root)
	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(1785628800000).UTC(), BackupID: testID(t, 82), ProductVersion: "test",
		ProductCommit: strings.Repeat("d", 40), Now: func() time.Time { return time.UnixMilli(1785628800001).UTC() },
	})
	if err != nil || result.Inspection.CurrentVersion != 9 || result.Backup == nil ||
		result.Backup.MigrationFromSchema == nil || *result.Backup.MigrationFromSchema != 3 ||
		result.Backup.MigrationToSchema == nil || *result.Backup.MigrationToSchema != 9 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.reader.Query(`PRAGMA table_info(reservations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found = found || name == "terminal_reason"
	}
	if err := rows.Err(); err != nil || !found {
		t.Fatalf("terminal_reason=%v err=%v", found, err)
	}
}

func TestMigrateVersionFiveKeepsExistingProgramMetadataNull(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createVersionFiveDatabaseWithProgram(t, root)
	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(1785628800000).UTC(), BackupID: testID(t, 78), ProductVersion: "test",
		ProductCommit: strings.Repeat("e", 40), Now: func() time.Time { return time.UnixMilli(1785628800001).UTC() },
	})
	if err != nil || result.Inspection.CurrentVersion != 9 || result.Backup == nil ||
		result.Backup.MigrationFromSchema == nil || *result.Backup.MigrationFromSchema != 5 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var nullCount int
	if err := store.reader.QueryRow(`SELECT count(*) FROM program_revisions WHERE metadata IS NULL`).Scan(&nullCount); err != nil || nullCount != 1 {
		t.Fatalf("NULL count=%d err=%v", nullCount, err)
	}
	if _, err := store.writer.Exec(`UPDATE program_revisions SET metadata=x'00'`); err == nil {
		t.Fatal("migration後に既存revisionが更新されました")
	}
}

func TestMigrateVersionSevenAddsBasicRecordingDefaultsAndCanRestore(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations[:7] {
		if _, err := tx.Exec(item.content); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at_utc_ms) VALUES (?, ?, ?, 1)`,
			item.version, item.name, item.checksum[:]); err != nil {
			t.Fatal(err)
		}
	}
	backendID, serviceID := testID(t, 180), testID(t, 181)
	instanceID, revisionID, reservationID := testID(t, 182), testID(t, 183), testID(t, 184)
	identity, content := sha256.Sum256([]byte("schema-seven")), sha256.Sum256([]byte("program"))
	if _, err := tx.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 1, 1)`, backendID.Bytes(), identity[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO services
		(id, backend_instance_id, provider_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, 'service', 'VERIFIED', 1, 1)`, serviceID.Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO program_instances
		(id, service_id, provider_event_locator, raw_event_id, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, 'event', 4, 'VERIFIED', 1, 1)`, instanceID.Bytes(), serviceID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO program_revisions
		(id, program_instance_id, revision_number, content_hash, start_at_utc_ms, duration_ms, title, validation_state, created_at_utc_ms)
		VALUES (?, ?, 1, ?, 10000, 1800000, 'program', 'VALID', 1)`, revisionID.Bytes(), instanceID.Bytes(), content[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO reservations(
		id, version, state, program_instance_id, program_revision_id, backend_instance_id,
		provider_service_locator, tuning_target, network_id, transport_stream_id, service_id, event_id,
		title, station_name, start_at_utc_ms, duration_seconds, requested_priority, requested_follow,
		effective_follow, created_at_utc_ms, updated_at_utc_ms)
		VALUES (?, 1, 'ACTIVE', ?, ?, ?, 'service', 'service', 1, 2, 3, 4,
		'program', 'station', 10000, 1800, 3, 1, 0, 1, 1)`, reservationID.Bytes(), instanceID.Bytes(),
		revisionID.Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := InspectDatabase(context.Background(), root)
	if err != nil || before.State != StateBehind || before.CurrentVersion != 7 || before.TargetVersion != 9 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	result, err := MigrateDatabaseWithBackup(context.Background(), root, MigrationRequest{
		AppliedAt: time.UnixMilli(2).UTC(), BackupID: testID(t, 185), ProductVersion: "test",
		ProductCommit: strings.Repeat("f", 40), Now: func() time.Time { return time.UnixMilli(3).UTC() },
	})
	if err != nil || result.Inspection.CurrentVersion != 9 || result.Backup == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	store, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var enabled, defaults, startMargin, endMargin int
	var outputFolder, outputTemplate string
	if err := store.reader.QueryRow(`SELECT enabled, use_default_margins, effective_start_margin_seconds,
		effective_end_margin_seconds, output_folder, output_template FROM reservations WHERE id=?`, reservationID.Bytes()).
		Scan(&enabled, &defaults, &startMargin, &endMargin, &outputFolder, &outputTemplate); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || defaults != 1 || startMargin != 5 || endMargin != 2 || outputFolder != "" || outputTemplate != "" {
		t.Fatalf("enabled=%d default=%d margins=%d/%d output=%q/%q", enabled, defaults, startMargin, endMargin,
			outputFolder, outputTemplate)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := FindBackupManifest(root, testID(t, 185))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreOffline(context.Background(), root, RestoreRequest{
		OperationID: testID(t, 186), BackupManifest: manifest,
		CreatedAt: time.UnixMilli(4).UTC(), Now: func() time.Time { return time.UnixMilli(5).UTC() },
	})
	if err != nil || restored.Phase != RestoreCommitted {
		t.Fatalf("restore=%+v err=%v", restored, err)
	}
	after, err := InspectDatabase(context.Background(), root)
	if err != nil || after.CurrentVersion != 7 || after.State != StateBehind {
		t.Fatalf("after=%+v err=%v", after, err)
	}
}

func TestVersionTwoProviderKindsAndProgramCountLimit(t *testing.T) {
	_, store := openMigratedStore(t)
	for marker, kind := range map[byte]string{92: "FAKE", 93: "MIRAKURUN"} {
		if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
			ID: testID(t, marker), Kind: kind, IdentityHash: sha256.Sum256([]byte(kind)), ObservedAtMS: 1,
		}); err != nil {
			t.Fatalf("kind=%s err=%v", kind, err)
		}
	}
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: testID(t, 94), Kind: "UNKNOWN", IdentityHash: sha256.Sum256([]byte("unknown")), ObservedAtMS: 1,
	}); err == nil {
		t.Fatal("未承認のprovider kindが保存されました")
	}
	backendID := testID(t, 92)
	if _, err := store.writer.Exec(`INSERT INTO catalog_syncs
		(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, service_count, program_count, correlation_id)
		VALUES (?, ?, 'COMPLETED', 1, 2, 0, 262144, 'upper-bound')`, testID(t, 95).Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.Exec(`INSERT INTO catalog_syncs
		(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, service_count, program_count, correlation_id)
		VALUES (?, ?, 'COMPLETED', 1, 2, 0, 262145, 'one-over')`, testID(t, 96).Bytes(), backendID.Bytes()); err == nil {
		t.Fatal("番組数上限を1件超えた世代が保存されました")
	}
}

func TestVersionOneMigrationFailureLeavesDatabaseBehind(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string) context.Context
	}{
		{name: "backup cap", prepare: func(t *testing.T, root string) context.Context {
			backupRoot := filepath.Join(root, "backups")
			if err := os.Mkdir(backupRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			for index := 0; index < maxBackupEntries; index++ {
				path := filepath.Join(backupRoot, fmt.Sprintf("entry-%02d", index))
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			return context.Background()
		}},
		{name: "cancelled", prepare: func(_ *testing.T, _ string) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			createVersionOneDatabase(t, root, false)
			ctx := test.prepare(t, root)
			if _, err := MigrateDatabaseWithBackup(ctx, root, MigrationRequest{
				AppliedAt: time.UnixMilli(1785628800000).UTC(), BackupID: testID(t, 97), ProductVersion: "test",
				ProductCommit: strings.Repeat("b", 40), Now: time.Now,
			}); err == nil {
				t.Fatal("事前backup失敗後にmigrationが成功しました")
			}
			inspection, err := InspectDatabase(context.Background(), root)
			if err != nil || inspection.State != StateBehind || inspection.CurrentVersion != 1 {
				t.Fatalf("inspection=%+v err=%v", inspection, err)
			}
		})
	}
}

func createVersionOneDatabase(t *testing.T, root string, populated bool) {
	t.Helper()
	database, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrations[0].content); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at_utc_ms) VALUES (1, ?, ?, 1)`,
		migrations[0].name, migrations[0].checksum[:]); err != nil {
		t.Fatal(err)
	}
	if populated {
		backendID := testID(t, 88)
		identity := sha256.Sum256([]byte("version-one"))
		if _, err := tx.Exec(`INSERT INTO backend_instances
			(id, provider_kind, identity_hash, reported_version, source_ref, created_at_utc_ms, last_seen_at_utc_ms)
			VALUES (?, 'FAKE', ?, 'v1', 'synthetic', 1, 2)`, backendID.Bytes(), identity[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO catalog_syncs
			(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, service_count, program_count, correlation_id)
			VALUES (?, ?, 'COMPLETED', 3, 4, 0, 100000, 'version-one')`, testID(t, 89).Bytes(), backendID.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createVersionTwoDatabase(t *testing.T, root string) {
	t.Helper()
	database, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, item := range migrations[:2] {
		if _, err := tx.Exec(item.content); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at_utc_ms) VALUES (?, ?, ?, 1)`,
			item.version, item.name, item.checksum[:]); err != nil {
			t.Fatal(err)
		}
	}
	backendID := testID(t, 84)
	identityHash := sha256.Sum256([]byte("version-two"))
	if _, err := tx.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, reported_version, source_ref, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 'v2', 'synthetic', 1, 2)`, backendID.Bytes(), identityHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO catalog_syncs
		(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, service_count, program_count, correlation_id)
		VALUES (?, ?, 'COMPLETED', 3, 4, 0, 0, 'version-two')`, testID(t, 85).Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createVersionThreeDatabase(t *testing.T, root string) {
	t.Helper()
	database, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, item := range migrations[:3] {
		if _, err := tx.Exec(item.content); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at_utc_ms) VALUES (?, ?, ?, 1)`,
			item.version, item.name, item.checksum[:]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func createVersionFiveDatabaseWithProgram(t *testing.T, root string) {
	t.Helper()
	database, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, item := range migrations[:5] {
		if _, err := tx.Exec(item.content); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at_utc_ms) VALUES (?, ?, ?, 1)`,
			item.version, item.name, item.checksum[:]); err != nil {
			t.Fatal(err)
		}
	}
	backendID, serviceID, instanceID, revisionID := testID(t, 79), testID(t, 80), testID(t, 81), testID(t, 82)
	identityHash := sha256.Sum256([]byte("version-five"))
	if _, err := tx.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 1, 1)`, backendID.Bytes(), identityHash[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO services
		(id, backend_instance_id, provider_locator, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, 'service', 'VERIFIED', 1, 1)`, serviceID.Bytes(), backendID.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO program_instances
		(id, service_id, provider_event_locator, raw_event_id, identity_state, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, ?, 'event', 1, 'VERIFIED', 1, 1)`, instanceID.Bytes(), serviceID.Bytes()); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("revision"))
	if _, err := tx.Exec(`INSERT INTO program_revisions
		(id, program_instance_id, revision_number, content_hash, title, validation_state, created_at_utc_ms)
		VALUES (?, ?, 1, ?, '番組', 'VALID', 1)`, revisionID.Bytes(), instanceID.Bytes(), hash[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationStateClassification(t *testing.T) {
	t.Run("unversioned nonempty", func(t *testing.T) {
		database := openMemoryDatabase(t)
		if _, err := database.Exec(`CREATE TABLE legacy(value INTEGER)`); err != nil {
			t.Fatal(err)
		}
		got, err := inspect(context.Background(), database)
		if err != nil || got.State != StateUnversionedNonempty {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if _, err := migrate(context.Background(), database, 1); err == nil {
			t.Fatal("unversioned DBがmigrationされました")
		}
	})

	t.Run("future", func(t *testing.T) {
		database := openMemoryDatabase(t)
		if _, err := migrate(context.Background(), database, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO schema_migrations VALUES (10, 'future', zeroblob(32), 2)`); err != nil {
			t.Fatal(err)
		}
		got, err := inspect(context.Background(), database)
		if err != nil || got.State != StateFuture {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("checksum drift", func(t *testing.T) {
		database := openMemoryDatabase(t)
		if _, err := database.Exec(`CREATE TABLE schema_migrations(version INTEGER, name TEXT, checksum BLOB, applied_at_utc_ms INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO schema_migrations VALUES (1, 'catalog_v1', zeroblob(32), 1)`); err != nil {
			t.Fatal(err)
		}
		got, err := inspect(context.Background(), database)
		if err != nil || got.State != StateDrifted {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("gap", func(t *testing.T) {
		database := openMemoryDatabase(t)
		migration := mustEmbeddedMigration(t)
		if _, err := database.Exec(`CREATE TABLE schema_migrations(version INTEGER, name TEXT, checksum BLOB, applied_at_utc_ms INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO schema_migrations VALUES (1, ?, ?, 1), (3, 'gap', zeroblob(32), 1)`,
			migration.name, migration.checksum[:]); err != nil {
			t.Fatal(err)
		}
		got, err := inspect(context.Background(), database)
		if err != nil || got.State != StateGapped {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		database := openMemoryDatabase(t)
		migration := mustEmbeddedMigration(t)
		if _, err := database.Exec(`CREATE TABLE schema_migrations(version INTEGER, name TEXT, checksum BLOB, applied_at_utc_ms INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO schema_migrations VALUES (1, ?, ?, 1), (1, ?, ?, 1)`,
			migration.name, migration.checksum[:], migration.name, migration.checksum[:]); err != nil {
			t.Fatal(err)
		}
		got, err := inspect(context.Background(), database)
		if err != nil || got.State != StateGapped {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("invalid applied timestamp", func(t *testing.T) {
		database := openMemoryDatabase(t)
		migration := mustEmbeddedMigration(t)
		if _, err := database.Exec(`CREATE TABLE schema_migrations(version INTEGER, name TEXT, checksum BLOB, applied_at_utc_ms INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO schema_migrations VALUES (1, ?, ?, -1)`, migration.name, migration.checksum[:]); err != nil {
			t.Fatal(err)
		}
		got, err := inspect(context.Background(), database)
		if err != nil || got.State != StateGapped || got.Reason != "invalid-applied-timestamp" {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
}

func mustEmbeddedMigration(t *testing.T) migration {
	t.Helper()
	migrations, err := embeddedMigrations()
	if err != nil || len(migrations) == 0 {
		t.Fatalf("migrations=%d err=%v", len(migrations), err)
	}
	return migrations[0]
}

func TestMigrationAndRevisionTablesAreInsertOnly(t *testing.T) {
	_, store := openMigratedStore(t)
	if _, err := store.writer.Exec(`UPDATE schema_migrations SET name='changed' WHERE version=1`); err == nil {
		t.Fatal("schema_migrations updateが成功しました")
	}
	if _, err := store.writer.Exec(`DELETE FROM schema_migrations WHERE version=1`); err == nil {
		t.Fatal("schema_migrations deleteが成功しました")
	}
}

func TestMigrationContextRollback(t *testing.T) {
	database := openMemoryDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := migrate(ctx, database, 1); err == nil {
		t.Fatal("cancel済みcontextでmigrationが成功しました")
	}
	inspection, err := inspect(context.Background(), database)
	if err != nil || inspection.State != StateEmpty {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
}

func TestCurrentMigrationRefusesForeignKeyCorruption(t *testing.T) {
	database := openMemoryDatabase(t)
	if _, err := migrate(context.Background(), database, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO catalog_syncs
		(id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms, correlation_id)
		VALUES (randomblob(16), randomblob(16), 'COMPLETED', 1, 2, 'corrupt')`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate(context.Background(), database, 2); err == nil {
		t.Fatal("FK破損したCURRENT DBが成功扱いされました")
	}
}

func TestFixedDSNAndRootValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "name with space.sqlite3")
	writer, err := url.Parse(buildWriterDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if writer.Query().Get("_txlock") != "immediate" || writer.Query().Get("mode") != "" {
		t.Fatalf("writer query=%v", writer.Query())
	}
	if got := writer.Query()["_pragma"]; len(got) != 4 || strings.Join(got, ",") != "busy_timeout(5000),foreign_keys(1),journal_mode(wal),synchronous(full)" {
		t.Fatalf("writer pragmas=%v", got)
	}
	reader, err := url.Parse(buildReaderDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if reader.Query().Get("mode") != "ro" || reader.Query().Get("query_only") != "" {
		t.Fatalf("reader query=%v", reader.Query())
	}

	if _, err := openWriter(context.Background(), "relative"); err == nil {
		t.Fatal("relative data rootが受理されました")
	}
	openRoot := t.TempDir()
	if err := os.Chmod(openRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openWriter(context.Background(), openRoot); err == nil {
		t.Fatal("owner-onlyでないdata rootが受理されました")
	}
}

func TestStoreOwnerLockRefusesSecondStore(t *testing.T) {
	root, store := openMigratedStore(t)
	if _, err := OpenStore(context.Background(), root); err == nil {
		t.Fatal("同じdata rootの2つ目のStoreが開きました")
	}
	if _, err := MigrateDatabase(context.Background(), root, 1); err == nil {
		t.Fatal("Storeが開いている間にmigration commandが開始しました")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestContendingWriterHonorsContextDeadline(t *testing.T) {
	root, store := openMigratedStore(t)
	tx, err := store.writer.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	holderHash := sha256.Sum256([]byte("fake:lock-holder"))
	if _, err := tx.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 1, 1)`, testID(t, 28).Bytes(), holderHash[:]); err != nil {
		t.Fatal(err)
	}
	contender, err := openWriter(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	contenderHash := sha256.Sum256([]byte("fake:contender"))
	_, err = contender.ExecContext(ctx, `INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, 1, 1)`, testID(t, 29).Bytes(), contenderHash[:])
	if err == nil {
		t.Fatal("排他中の2つ目のwriterが成功しました")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context deadline後もwriterが待機しました: %s", elapsed)
	}
}

func TestOnlyOneRunningSyncPerBackend(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := testID(t, 20)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:single-worker")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	first := testID(t, 21)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: first, BackendID: backendID, StartedAtMS: 2, CorrelationID: "first",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: testID(t, 22), BackendID: backendID, StartedAtMS: 3, CorrelationID: "duplicate",
	}); err == nil {
		t.Fatal("同じbackendの重複RUNNING syncが作られました")
	}
	if err := store.CompleteSync(context.Background(), first, 4, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: testID(t, 23), BackendID: backendID, StartedAtMS: 5, CorrelationID: "next",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncCompletionRejectsObservationCountMismatch(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := testID(t, 26)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:count-mismatch")), ObservedAtMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	syncID := testID(t, 27)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: 2, CorrelationID: "count-mismatch",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, 3, 1, 0); err == nil {
		t.Fatal("保存件数と一致しないgenerationがCOMPLETEDになりました")
	}
	var state string
	if err := store.reader.QueryRow(`SELECT state FROM catalog_syncs WHERE id=?`, syncID.Bytes()).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "RUNNING" {
		t.Fatalf("mismatch後のstate=%s", state)
	}
}

func TestSyncRejectsBackwardTimeAndInvalidUTF8(t *testing.T) {
	_, store := openMigratedStore(t)
	backendID := testID(t, 30)
	if err := store.EnsureBackend(context.Background(), catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:validation")), ObservedAtMS: 10,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: testID(t, 31), BackendID: backendID, StartedAtMS: 10, CorrelationID: string([]byte{0xff}),
	}); err == nil {
		t.Fatal("invalid UTF-8 correlationが受理されました")
	}
	syncID := testID(t, 32)
	if err := store.BeginSync(context.Background(), catalogmodel.Sync{
		ID: syncID, BackendID: backendID, StartedAtMS: 10, CorrelationID: "validation",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSync(context.Background(), syncID, 9, 0, 0); err == nil {
		t.Fatal("開始前の時刻でsyncがCOMPLETEDになりました")
	}
	if err := store.FailSync(context.Background(), syncID, 9, "sync-failed"); err == nil {
		t.Fatal("開始前の時刻でsyncがFAILEDになりました")
	}
	if err := store.StoreServices(context.Background(), syncID, []catalogmodel.ServiceObservation{{
		ProviderLocator: string([]byte{0xff}), DisplayName: "invalid", Validation: catalogmodel.ValidationInvalid,
	}}); err == nil {
		t.Fatal("invalid UTF-8 observationが保存されました")
	}
	if err := store.FailSync(context.Background(), syncID, 10, "sync-failed"); err != nil {
		t.Fatal(err)
	}
}

func TestDataRootSymlinkIsRejected(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openWriter(context.Background(), link); err == nil {
		t.Fatal("symlink data rootが受理されました")
	}
}

func TestEmbeddedMigrationChecksumAndLimits(t *testing.T) {
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[0].version != 1 || migrations[0].name != "catalog_v1" ||
		migrations[1].version != 2 || migrations[1].name != "mirakurun_catalog_limits" ||
		migrations[2].version != 3 || migrations[2].name != "first_recording" ||
		migrations[3].version != 4 || migrations[3].name != "reservation_terminal_reason" ||
		migrations[4].version != 5 || migrations[4].name != "automatic_reservations" ||
		migrations[5].version != 6 || migrations[5].name != "program_metadata" ||
		migrations[6].version != 7 || migrations[6].name != "user_stopped_recording" ||
		migrations[7].version != 8 || migrations[7].name != "basic_recording_settings" ||
		migrations[8].version != 9 || migrations[8].name != "reservation_output" {
		t.Fatalf("migrations=%+v", migrations)
	}
	if got := sha256.Sum256([]byte(migrations[0].content)); got != migrations[0].checksum {
		t.Fatal("embedded migration checksumが一致しません")
	}
	for _, forbidden := range []string{"VACUUM", "ATTACH", "DETACH", "load_extension"} {
		if strings.Contains(strings.ToUpper(migrations[0].content), strings.ToUpper(forbidden)) {
			t.Fatalf("禁止SQL %sを検出しました", forbidden)
		}
	}
}

func TestCurrentCatalogQueriesUseBoundedIndexes(t *testing.T) {
	_, store := openMigratedStore(t)
	assertQueryPlanUses(t, store.reader, "catalog_syncs_completed_backend_idx", `
		SELECT id FROM catalog_syncs
		WHERE backend_instance_id=? AND state='COMPLETED'
		ORDER BY finished_at_utc_ms DESC, id DESC LIMIT 1`, make([]byte, 16))
	assertQueryPlanUses(t, store.reader, "sqlite_autoindex_program_revisions_2", `
		SELECT id, content_hash, revision_number FROM program_revisions
		WHERE program_instance_id=? ORDER BY revision_number DESC LIMIT 1`, make([]byte, 16))
}

func assertQueryPlanUses(t *testing.T, database *sql.DB, index, query string, arguments ...any) {
	t.Helper()
	rows, err := database.Query("EXPLAIN QUERY PLAN "+query, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	details := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(details, index) {
		t.Fatalf("query planがindex %sを使いませんでした: %s", index, details)
	}
}

func openMemoryDatabase(t *testing.T) *sql.DB {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	database, err := sql.Open("sqlite3", "file:"+name+"?mode=memory&cache=shared&_txlock=immediate&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestSanitizeDoesNotExposeDriverError(t *testing.T) {
	err := sanitize("operation", errors.New("private path and SQL"))
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SQL") {
		t.Fatalf("raw errorが露出しました: %v", err)
	}
}
