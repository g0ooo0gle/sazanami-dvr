package sqlite

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

func TestIncrementalBackupPublishAndVerify(t *testing.T) {
	root, store := openMigratedStore(t)
	if _, err := store.writer.Exec(`INSERT INTO backend_instances
		(id, provider_kind, identity_hash, created_at_utc_ms, last_seen_at_utc_ms)
		VALUES (randomblob(16), 'FAKE', randomblob(32), 1, 1)`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 2, 1, 2, 3, 4, time.UTC)
	id, err := catalogmodel.NewIDFrom(bytes.NewReader(make([]byte, 16)))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.CreateBackup(context.Background(), BackupRequest{
		ID: id, Purpose: "manual", StartedAt: start, ProductVersion: "development",
		ProductCommit: strings.Repeat("a", 40), Now: func() time.Time { return start.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Format != backupManifestFormat || manifest.SchemaVersion != 8 || manifest.DatabaseBytes < 1 ||
		manifest.DatabaseSHA256 == "" || manifest.PageCount < 1 || manifest.JournalMode != "wal" ||
		manifest.Synchronous != "full" || !manifest.ForeignKeys || manifest.CGOEnabled {
		t.Fatalf("manifest=%+v", manifest)
	}
	backupRoot := filepath.Join(root, "backups")
	entries, err := os.ReadDir(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%v", entries)
	}
	manifestName := strings.TrimSuffix(manifest.DatabaseFile, ".sqlite3") + ".manifest.json"
	verified, err := VerifyBackup(context.Background(), root, manifestName)
	if err != nil || verified.DatabaseSHA256 != manifest.DatabaseSHA256 {
		t.Fatalf("verified=%+v err=%v", verified, err)
	}
	for _, name := range []string{manifest.DatabaseFile, manifestName} {
		info, err := os.Stat(filepath.Join(backupRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o", name, info.Mode().Perm())
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".catalog-") {
			t.Fatalf("partial artifactが公開後に残りました: %s", entry.Name())
		}
	}
}

func TestBackupTamperAndCancelledOperationFailClosed(t *testing.T) {
	t.Run("tampered database", func(t *testing.T) {
		root, store := openMigratedStore(t)
		start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
		id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{1}, 16)))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := store.CreateBackup(context.Background(), BackupRequest{
			ID: id, Purpose: "manual", StartedAt: start, ProductVersion: "development",
			ProductCommit: strings.Repeat("b", 40), Now: func() time.Time { return start },
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "backups", manifest.DatabaseFile)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("tamper")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		manifestName := strings.TrimSuffix(manifest.DatabaseFile, ".sqlite3") + ".manifest.json"
		if _, err := VerifyBackup(context.Background(), root, manifestName); err == nil {
			t.Fatal("改ざん済みbackupが検証を通過しました")
		}
	})

	t.Run("manifest permission", func(t *testing.T) {
		root, store := openMigratedStore(t)
		start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
		id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{3}, 16)))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := store.CreateBackup(context.Background(), BackupRequest{
			ID: id, Purpose: "manual", StartedAt: start, ProductVersion: "development",
			ProductCommit: strings.Repeat("d", 40), Now: func() time.Time { return start },
		})
		if err != nil {
			t.Fatal(err)
		}
		manifestName := strings.TrimSuffix(manifest.DatabaseFile, ".sqlite3") + ".manifest.json"
		if err := os.Chmod(filepath.Join(root, "backups", manifestName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBackup(context.Background(), root, manifestName); err == nil {
			t.Fatal("owner-onlyでないmanifestが検証を通過しました")
		}
	})

	t.Run("duplicate manifest key", func(t *testing.T) {
		root, store := openMigratedStore(t)
		start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
		id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{4}, 16)))
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := store.CreateBackup(context.Background(), BackupRequest{
			ID: id, Purpose: "manual", StartedAt: start, ProductVersion: "development",
			ProductCommit: strings.Repeat("e", 40), Now: func() time.Time { return start },
		})
		if err != nil {
			t.Fatal(err)
		}
		manifestName := strings.TrimSuffix(manifest.DatabaseFile, ".sqlite3") + ".manifest.json"
		path := filepath.Join(root, "backups", manifestName)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = bytes.Replace(data, []byte("{"), []byte(`{"format":"duplicate",`), 1)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBackup(context.Background(), root, manifestName); err == nil {
			t.Fatal("重複keyを持つmanifestが検証を通過しました")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		root, store := openMigratedStore(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
		id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{2}, 16)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateBackup(ctx, BackupRequest{
			ID: id, Purpose: "manual", StartedAt: start, ProductVersion: "development",
			ProductCommit: strings.Repeat("c", 40), Now: func() time.Time { return start },
		}); err == nil {
			t.Fatal("cancel済みbackupが成功しました")
		}
		entries, err := os.ReadDir(filepath.Join(root, "backups"))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".manifest.json") && !strings.HasPrefix(entry.Name(), ".") {
				t.Fatalf("cancel済みbackupのfinal manifestが公開されました: %s", entry.Name())
			}
		}
	})
}

func TestBackupInputAndCaps(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	backupRoot, err := prepareBackupRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxCompletedBackups; index++ {
		name := filepath.Join(backupRoot, "catalog-test-"+strings.Repeat("x", index)+".manifest.json")
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := enforceBackupCaps(backupRoot); err == nil {
		t.Fatal("completed backup capが適用されませんでした")
	}
	if _, err := VerifyBackup(context.Background(), root, "../escape.manifest.json"); err == nil {
		t.Fatal("path traversalが受理されました")
	}
	if err := validateBackupRequest(BackupRequest{Purpose: "manual"}); err == nil {
		t.Fatal("不完全なrequestが受理されました")
	}
}

func TestBackupCapacityOverflowFailsClosed(t *testing.T) {
	if err := requireBackupFreeSpace(t.TempDir(), int(^uint(0)>>1), int(^uint(0)>>1)); err == nil {
		t.Fatal("容量計算overflowが受理されました")
	}
}

func TestHashRejectsHardLinkedArtifact(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.sqlite3")
	if err := os.WriteFile(source, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, filepath.Join(root, "second.sqlite3")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hashFile(source); err == nil {
		t.Fatal("hard linkを持つartifactが受理されました")
	}
}
