package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var setupStoreAppliedAt = time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)

func TestOpenStoreForSetupInitializesEmptyAndKeepsOwnerLock(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}

	store, inspection, err := OpenStoreForSetup(context.Background(), root, setupStoreAppliedAt)
	if err != nil {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	if store == nil || inspection.State != StateCurrent || inspection.CurrentVersion != 14 {
		t.Fatalf("store=%v inspection=%+v", store != nil, inspection)
	}
	if _, err := acquireOwnerLock(root); err == nil {
		t.Fatal("setup Storeのowner lockが保持されていません")
	}
	if _, err := os.Stat(filepath.Join(root, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty setupがbackupを作成しました: err=%v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireOwnerLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseOwnerLock(lock); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreForSetupInitializesZeroLengthDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, databaseFilename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	store, inspection, err := OpenStoreForSetup(context.Background(), root, setupStoreAppliedAt)
	if err != nil {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	if store == nil || inspection.State != StateCurrent {
		t.Fatalf("store=%v inspection=%+v", store != nil, inspection)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreForSetupKeepsCurrentSchema(t *testing.T) {
	root, store := openMigratedStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := InspectDatabase(context.Background(), root)
	if err != nil || before.State != StateCurrent || before.CurrentVersion != 14 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	setupStore, after, err := OpenStoreForSetup(context.Background(), root, setupStoreAppliedAt)
	if err != nil {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	if setupStore == nil || after.State != StateCurrent || after.CurrentVersion != before.CurrentVersion {
		t.Fatalf("store=%v before=%+v after=%+v", setupStore != nil, before, after)
	}
	var migrationCount int
	if err := setupStore.reader.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != before.CurrentVersion {
		t.Fatalf("migration count=%d, want=%d", migrationCount, before.CurrentVersion)
	}
	if err := setupStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreForSetupRefusesNonCurrentWithoutChangingDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	createVersionOneDatabase(t, root, true)
	databasePath := filepath.Join(root, databaseFilename)
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	store, inspection, err := OpenStoreForSetup(context.Background(), root, setupStoreAppliedAt)
	if store != nil {
		t.Fatal("non-current DBからStoreを開きました")
	}
	if !errors.Is(err, ErrSetupDatabaseNotCurrent) || inspection.State != StateBehind {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("non-current DBが変更されました")
	}
	lock, err := acquireOwnerLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseOwnerLock(lock); err != nil {
		t.Fatal(err)
	}
}

func TestOpenStoreForSetupMapsUnreadableInspectionToFixedError(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, databaseFilename), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, inspection, err := OpenStoreForSetup(context.Background(), root, setupStoreAppliedAt)
	if store != nil || !errors.Is(err, ErrSetupDatabaseNotCurrent) || inspection.State != StateUnreadable {
		t.Fatalf("store=%v inspection=%+v err=%v", store != nil, inspection, err)
	}
}

func TestOpenStoreForSetupRefusesOwnerLockContention(t *testing.T) {
	root, store := openMigratedStore(t)
	defer store.Close()

	second, inspection, err := OpenStoreForSetup(context.Background(), root, setupStoreAppliedAt)
	if second != nil || inspection.State != StateUnreadable || err == nil {
		t.Fatalf("second=%v inspection=%+v err=%v", second != nil, inspection, err)
	}
}
