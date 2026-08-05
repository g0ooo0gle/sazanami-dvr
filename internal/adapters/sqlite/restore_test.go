package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestOfflineRestoreQuarantinesOldDatabaseAndCommits(t *testing.T) {
	root, store := openMigratedStore(t)
	insertBackendRow(t, store, 1)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	backupID := testID(t, 4)
	backup, err := store.CreateBackup(context.Background(), BackupRequest{
		ID: backupID, Purpose: "manual", StartedAt: start, ProductVersion: "development",
		ProductCommit: strings.Repeat("d", 40), Now: func() time.Time { return start.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	insertBackendRow(t, store, 2)
	var before int
	if err := store.reader.QueryRow(`SELECT count(*) FROM backend_instances`).Scan(&before); err != nil || before != 2 {
		t.Fatalf("before=%d err=%v", before, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	now := start.Add(2 * time.Second)
	operationID := testID(t, 5)
	operation, err := RestoreOffline(context.Background(), root, RestoreRequest{
		OperationID:    operationID,
		BackupManifest: strings.TrimSuffix(backup.DatabaseFile, ".sqlite3") + ".manifest.json",
		CreatedAt:      now,
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if operation.Phase != RestoreCommitted || operation.InstalledDatabaseSHA256 == nil ||
		*operation.InstalledDatabaseSHA256 != backup.DatabaseSHA256 || operation.FailureReason != nil {
		t.Fatalf("operation=%+v", operation)
	}
	operationPath := filepath.Join(root, RestoreOperationBasename(operationID))
	persisted, err := readRestoreOperation(operationPath)
	if err != nil || persisted.Phase != RestoreCommitted || persisted.Revision < 7 {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	oldPath := filepath.Join(root, ".restore-"+operationID.String()+".old.sqlite3")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("旧DBがquarantineされていません: %v", err)
	}
	if pathExists(filepath.Join(root, ".restore-"+operationID.String()+".staged.sqlite3")) {
		t.Fatal("commit後にstaged DBが残りました")
	}

	restored, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	var after int
	if err := restored.reader.QueryRow(`SELECT count(*) FROM backend_instances`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("restore後count=%d, want=1", after)
	}
}

func TestOfflineRestoreCancellationBeforeQuarantineKeepsCanonical(t *testing.T) {
	root, store := openMigratedStore(t)
	insertBackendRow(t, store, 1)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	backup := createTestBackup(t, store, testID(t, 6), start)
	insertBackendRow(t, store, 2)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	beforeHash, _, err := hashFile(filepath.Join(root, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	operationID := testID(t, 7)
	called := false
	operation, err := RestoreOffline(ctx, root, RestoreRequest{
		OperationID:    operationID,
		BackupManifest: strings.TrimSuffix(backup.DatabaseFile, ".sqlite3") + ".manifest.json",
		CreatedAt:      start.Add(time.Minute),
		Now: func() time.Time {
			if !called {
				called = true
				cancel()
			}
			return start.Add(time.Minute)
		},
	})
	if err == nil || operation.Phase != RestoreRolledBack {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
	afterHash, _, err := hashFile(filepath.Join(root, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	if beforeHash != afterHash {
		t.Fatal("quarantine前cancelでcanonical DBが変わりました")
	}
	if pathExists(filepath.Join(root, ".restore-"+operationID.String()+".old.sqlite3")) {
		t.Fatal("quarantine前cancelで旧DBが移動しました")
	}
}

func TestRestoreRefusesNonterminalOperation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	id := testID(t, 8)
	digest := strings.Repeat("a", 64)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	operation := RestoreOperation{
		Format: restoreOperationFormat, FormatVersion: 1, OperationID: id.String(), Revision: 1,
		Phase: RestorePrepared, CreatedAtUTC: stamp, UpdatedAtUTC: stamp,
		SourceBackupID: id.String(), SourceDatabaseFile: "catalog-synthetic-" + id.String() + ".sqlite3",
		SourceDatabaseSHA256: digest, TargetDatabaseFile: databaseFilename,
		StagedDatabaseFile: ".restore-" + id.String() + ".staged.sqlite3", StagedDatabaseSHA256: digest,
	}
	if err := publishRestoreOperation(root, operation); err != nil {
		t.Fatal(err)
	}
	if err := enforceRestoreCaps(root); err == nil {
		t.Fatal("nonterminal operationが見逃されました")
	}
}

func TestRestoreRefusesOpenStore(t *testing.T) {
	root, store := openMigratedStore(t)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	backup := createTestBackup(t, store, testID(t, 24), start)
	_, err := RestoreOffline(context.Background(), root, RestoreRequest{
		OperationID: testID(t, 25), BackupManifest: strings.TrimSuffix(backup.DatabaseFile, ".sqlite3") + ".manifest.json",
		CreatedAt: start, Now: func() time.Time { return start },
	})
	if err == nil {
		t.Fatal("Storeが開いている間にrestoreが開始しました")
	}
}

func TestRestoreOperationRejectsDuplicateKeyAndPathEscape(t *testing.T) {
	root, store := openMigratedStore(t)
	insertBackendRow(t, store, 1)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	backup := createTestBackup(t, store, testID(t, 50), start)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	operationID := testID(t, 51)
	operation := prepareInterruptedRestore(t, root, backup, operationID, RestorePrepared)
	path := filepath.Join(root, RestoreOperationBasename(operationID))

	t.Run("duplicate key", func(t *testing.T) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte("{"), []byte(`{"phase":"COMMITTED",`), 1)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRestoreOperation(path); err == nil {
			t.Fatal("重複keyを持つoperationが受理されました")
		}
	})

	t.Run("path escape", func(t *testing.T) {
		operation.StagedDatabaseFile = "../outside.sqlite3"
		data, err := json.Marshal(operation)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, '\n')
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRestoreOperation(path); err == nil {
			t.Fatal("root外を指すoperationが受理されました")
		}
	})
}

func TestRecoverRestoreFromOldQuarantined(t *testing.T) {
	root, store := openMigratedStore(t)
	insertBackendRow(t, store, 1)
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	backup := createTestBackup(t, store, testID(t, 9), start)
	insertBackendRow(t, store, 2)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	operationID := testID(t, 10)
	operation := prepareInterruptedRestore(t, root, backup, operationID, RestoreOldQuarantined)
	now := start.Add(time.Minute)
	recovered, err := RecoverRestoreOffline(context.Background(), root, RestoreOperationBasename(operationID), func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	if err != nil || recovered.Phase != RestoreCommitted {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	restored, err := OpenStore(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	var count int
	if err := restored.reader.QueryRow(`SELECT count(*) FROM backend_instances`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v operation=%+v", count, err, operation)
	}
}

func TestRecoverRestoreKillMatrix(t *testing.T) {
	tests := []struct {
		phase RestorePhase
		want  RestorePhase
	}{
		{RestorePrepared, RestoreRolledBack},
		{RestoreQuarantining, RestoreRolledBack},
		{RestoreOldQuarantined, RestoreCommitted},
		{RestoreInstalling, RestoreCommitted},
		{RestoreNewInstalled, RestoreCommitted},
		{RestoreVerified, RestoreCommitted},
		{RestoreRollingBack, RestoreRolledBack},
	}
	for index, test := range tests {
		t.Run(string(test.phase), func(t *testing.T) {
			root, store := openMigratedStore(t)
			insertBackendRow(t, store, 1)
			start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
			backup := createTestBackup(t, store, testID(t, byte(30+index*2)), start)
			insertBackendRow(t, store, 2)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			operationID := testID(t, byte(31+index*2))
			prepareInterruptedRestore(t, root, backup, operationID, test.phase)
			now := start.Add(time.Minute)
			recovered, err := RecoverRestoreOffline(context.Background(), root, RestoreOperationBasename(operationID), func() time.Time {
				now = now.Add(time.Second)
				return now
			})
			if recovered.Phase != test.want {
				t.Fatalf("phase=%s want=%s err=%v", recovered.Phase, test.want, err)
			}
			if test.want == RestoreCommitted && err != nil {
				t.Fatal(err)
			}
			if test.want == RestoreRolledBack && test.phase != RestorePrepared && err == nil {
				t.Fatal("rollback原因が返りませんでした")
			}
			opened, openErr := OpenStore(context.Background(), root)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer opened.Close()
			var count int
			if queryErr := opened.reader.QueryRow(`SELECT count(*) FROM backend_instances`).Scan(&count); queryErr != nil {
				t.Fatal(queryErr)
			}
			wantCount := 1
			if test.want == RestoreRolledBack {
				wantCount = 2
			}
			if count != wantCount {
				t.Fatalf("backend count=%d want=%d", count, wantCount)
			}
		})
	}
}

func insertBackendRow(t *testing.T, store *Store, marker byte) {
	t.Helper()
	id := bytes.Repeat([]byte{marker}, 16)
	hash := bytes.Repeat([]byte{marker}, 32)
	if _, err := store.writer.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (?, 'FAKE', ?, ?, ?)`, id, hash, int64(marker), int64(marker)); err != nil {
		t.Fatal(err)
	}
}

func createTestBackup(t *testing.T, store *Store, id catalogmodel.ID, start time.Time) BackupManifest {
	t.Helper()
	manifest, err := store.CreateBackup(context.Background(), BackupRequest{
		ID: id, Purpose: "manual", StartedAt: start, ProductVersion: "development",
		ProductCommit: strings.Repeat("e", 40), Now: func() time.Time { return start.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func testID(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{marker}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func prepareInterruptedRestore(t *testing.T, root string, backup BackupManifest, operationID catalogmodel.ID, phase RestorePhase) RestoreOperation {
	t.Helper()
	id := operationID.String()
	stagedName := ".restore-" + id + ".staged.sqlite3"
	if err := copyFileExclusive(context.Background(), filepath.Join(root, "backups", backup.DatabaseFile),
		filepath.Join(root, stagedName), backup.DatabaseBytes); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, databaseFilename)
	present, oldHash, wal, shm, err := inspectCanonicalArtifacts(target)
	if err != nil || !present {
		t.Fatalf("old artifacts present=%v err=%v", present, err)
	}
	oldName := ".restore-" + id + ".old.sqlite3"
	if phase != RestorePrepared {
		if err := os.Rename(target, filepath.Join(root, oldName)); err != nil {
			t.Fatal(err)
		}
		if wal {
			if err := os.Rename(target+"-wal", filepath.Join(root, oldName+"-wal")); err != nil {
				t.Fatal(err)
			}
		}
		if shm {
			if err := os.Rename(target+"-shm", filepath.Join(root, oldName+"-shm")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if phase == RestoreNewInstalled || phase == RestoreVerified || phase == RestoreRollingBack {
		if err := os.Rename(filepath.Join(root, stagedName), target); err != nil {
			t.Fatal(err)
		}
	}
	operation := RestoreOperation{
		Format: restoreOperationFormat, FormatVersion: 1, OperationID: id, Revision: 3, Phase: phase,
		CreatedAtUTC: backup.CompletedAtUTC, UpdatedAtUTC: backup.CompletedAtUTC,
		SourceBackupID: backup.BackupID, SourceDatabaseFile: backup.DatabaseFile,
		SourceDatabaseSHA256: backup.DatabaseSHA256, TargetDatabaseFile: databaseFilename,
		StagedDatabaseFile: stagedName, StagedDatabaseSHA256: backup.DatabaseSHA256,
		OldDatabasePresent: true, OldDatabaseSHA256: oldHash, OldWALPresent: wal, OldSHMPresent: shm,
	}
	if phase == RestoreNewInstalled || phase == RestoreVerified {
		installed := backup.DatabaseSHA256
		operation.InstalledDatabaseSHA256 = &installed
	}
	if phase == RestoreRollingBack {
		reason := "restore-failed"
		operation.FailureReason = &reason
	}
	if err := publishRestoreOperation(root, operation); err != nil {
		t.Fatal(err)
	}
	return operation
}
