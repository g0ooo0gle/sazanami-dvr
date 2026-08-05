package catalogsync_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteadapter "github.com/g0ooo0gle/sazanami-dvr/internal/adapters/sqlite"
	"github.com/g0ooo0gle/sazanami-dvr/internal/app/catalogsync"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

const (
	targetSoakRootEnvironment     = "SAZANAMI_HF05A_SOAK_ROOT"
	targetSoakDurationEnvironment = "SAZANAMI_HF05A_SOAK_DURATION"
	targetSoakInterval            = time.Second
	targetSoakMaximumDuration     = 25 * time.Hour
)

// TestTargetFakeCatalogSoakは、明示指定した空のowner-only領域だけを使う実機向け長時間試験である。
// 通常のunit testとCIでは環境変数がないためskipし、外部providerやtunerへ接続しない。
func TestTargetFakeCatalogSoak(t *testing.T) {
	root := os.Getenv(targetSoakRootEnvironment)
	durationText := os.Getenv(targetSoakDurationEnvironment)
	if root == "" && durationText == "" {
		t.Skip("実機soak testは明示指定された場合だけ実行します")
	}
	if root == "" || durationText == "" {
		t.Fatal("soak rootとdurationは両方必要です")
	}
	duration, err := time.ParseDuration(durationText)
	if err != nil || duration < time.Second || duration > targetSoakMaximumDuration {
		t.Fatalf("soak durationは1秒以上25時間以下で指定してください: %q", durationText)
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("soak rootには正規化済み絶対pathが必要です")
	}
	if err := requireEmptySoakRoot(root); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Now().UTC()
	if _, err := sqliteadapter.MigrateDatabase(context.Background(), root, startedAt.UnixMilli()); err != nil {
		t.Fatalf("soak DBをmigrateできませんでした: %v", err)
	}
	store, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatalf("soak DBを開けませんでした: %v", err)
	}
	storeOpen := true
	t.Cleanup(func() {
		if storeOpen {
			_ = store.Close()
		}
	})

	backendID := mustCatalogID(t, 70)
	backend := catalogmodel.Backend{
		ID: backendID, Kind: "FAKE", IdentityHash: sha256.Sum256([]byte("fake:target-soak")),
	}
	deadline := startedAt.Add(duration)
	iterations := 0
	maximumLockWait := time.Duration(0)
	lastSample := startedAt
	lastTitle := ""

	for time.Now().UTC().Before(deadline) {
		lastTitle = fmt.Sprintf("実機合成番組-%06d", iterations)
		harness := newHarness(t, lastTitle, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, syncErr := (catalogsync.Service{
			Provider: harness.Catalog, Repository: store, Clock: realtimeClock{},
		}).Sync(ctx, catalogsync.Request{
			Backend: backend, CorrelationID: fmt.Sprintf("target-soak-%06d", iterations),
			ServicePageLimit: 16, ProgramPageLimit: 16, VerifiedFakeLineage: true,
		})
		cancel()
		if syncErr != nil || result.Services != 1 || result.Programs != 1 {
			t.Fatalf("iteration=%d result=%+v err=%v", iterations, result, syncErr)
		}
		programs, queryErr := store.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
		if queryErr != nil || len(programs) != 1 || programs[0].Material.Title == nil || *programs[0].Material.Title != lastTitle {
			t.Fatalf("iteration=%d current=%+v err=%v", iterations, programs, queryErr)
		}
		iterations++

		if iterations%60 == 0 {
			lockStarted := time.Now()
			second, lockErr := sqliteadapter.OpenStore(context.Background(), root)
			lockWait := time.Since(lockStarted)
			if second != nil {
				_ = second.Close()
			}
			if lockErr == nil {
				t.Fatal("owner lock保持中に2つ目のStoreが開きました")
			}
			if lockWait > maximumLockWait {
				maximumLockWait = lockWait
			}
		}

		now := time.Now().UTC()
		if now.Sub(lastSample) >= time.Minute {
			databaseBytes, walBytes, shmBytes := soakFileSizes(root)
			t.Logf("soak_sample elapsed=%s iterations=%d db_bytes=%d wal_bytes=%d shm_bytes=%d max_lock_wait=%s",
				now.Sub(startedAt).Round(time.Second), iterations, databaseBytes, walBytes, shmBytes, maximumLockWait)
			lastSample = now
		}

		next := startedAt.Add(time.Duration(iterations) * targetSoakInterval)
		if next.After(deadline) {
			next = deadline
		}
		if wait := time.Until(next); wait > 0 {
			time.Sleep(wait)
		}
	}
	if iterations == 0 {
		t.Fatal("soak iterationが1回も完了しませんでした")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("soak DBを閉じられませんでした: %v", err)
	}
	storeOpen = false

	restarted, err := sqliteadapter.OpenStore(context.Background(), root)
	if err != nil {
		t.Fatalf("soak DBを再起動できませんでした: %v", err)
	}
	programs, err := restarted.CurrentPrograms(context.Background(), backendID, 16, catalogmodel.ID{})
	closeErr := restarted.Close()
	if err != nil || closeErr != nil || len(programs) != 1 || programs[0].Material.Title == nil || *programs[0].Material.Title != lastTitle {
		t.Fatalf("再起動readbackに失敗しました: programs=%+v query_err=%v close_err=%v", programs, err, closeErr)
	}
	databaseBytes, walBytes, shmBytes := soakFileSizes(root)
	t.Logf("soak_summary duration=%s iterations=%d db_bytes=%d wal_bytes=%d shm_bytes=%d max_lock_wait=%s",
		time.Since(startedAt).Round(time.Second), iterations, databaseBytes, walBytes, shmBytes, maximumLockWait)
}

// requireEmptySoakRootは既存データや権限を変更せず、空の0700 directoryだけを受理する。
func requireEmptySoakRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("soak rootはsymlinkでない0700 directoryである必要があります")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("soak rootを確認できません: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("soak rootは空である必要があります")
	}
	return nil
}

func TestRequireEmptySoakRootFailsClosed(t *testing.T) {
	t.Run("空の0700 directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := requireEmptySoakRoot(root); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("広すぎる権限", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := requireEmptySoakRoot(root); err == nil {
			t.Fatal("0755 directoryを受理しました")
		}
	})
	t.Run("既存file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "existing"), []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := requireEmptySoakRoot(root); err == nil {
			t.Fatal("既存fileを含むdirectoryを受理しました")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "root")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := requireEmptySoakRoot(link); err == nil {
			t.Fatal("symlinkを受理しました")
		}
	})
}

// soakFileSizesはcatalog DBとWAL／SHMの現在sizeを返し、未作成sidecarは0として扱う。
func soakFileSizes(root string) (databaseBytes, walBytes, shmBytes int64) {
	return soakFileSize(filepath.Join(root, "catalog.sqlite3")),
		soakFileSize(filepath.Join(root, "catalog.sqlite3-wal")),
		soakFileSize(filepath.Join(root, "catalog.sqlite3-shm"))
}

func soakFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

type realtimeClock struct{}

func (realtimeClock) Now() time.Time { return time.Now().UTC() }
