//go:build unix

package recordingfs

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestPartialFileIsPublishedWithoutOverwrite(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "recordings")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan, err := recording.NewFilePlan(time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), fileID(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.CreatePartial(plan)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0x47}, 188)
	if written, err := file.Write(data); err != nil || written != len(data) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.CreatePartial(plan); !errors.Is(err, ErrPartialExists) {
		t.Fatalf("同名partialの再作成err=%v", err)
	}
	if err := root.LinkFinal(plan); err != nil {
		t.Fatal(err)
	}
	if err := root.LinkFinal(plan); !errors.Is(err, ErrFinalExists) {
		t.Fatalf("同名finalへの再公開err=%v", err)
	}
	if err := root.SyncDirectory(plan); err != nil {
		t.Fatal(err)
	}
	if err := root.RemovePartial(plan); err != nil {
		t.Fatal(err)
	}
	if err := root.SyncDirectory(plan); err != nil {
		t.Fatal(err)
	}
	partialPath := filepath.Join(rootPath, filepath.FromSlash(plan.PartialPath))
	if _, err := os.Lstat(partialPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partialが残っています: %v", err)
	}
	finalPath := filepath.Join(rootPath, filepath.FromSlash(plan.FinalPath))
	got, err := os.ReadFile(finalPath)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("final bytes=%d err=%v", len(got), err)
	}
	info, err := os.Lstat(finalPath)
	if err != nil || !validRegularInfo(info, 1) {
		t.Fatalf("final info=%+v err=%v", info, err)
	}
	observation, err := root.Inspect(plan)
	if err != nil || observation.Partial.Exists || !observation.Final.Exists || !observation.Final.Regular ||
		observation.Final.Size != 188 || observation.SameFile || observation.Unsafe {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
}

func TestFinalFileIsNeverReplaced(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "recordings")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan, err := recording.NewFilePlan(time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), fileID(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.CreatePartial(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bytes.Repeat([]byte{0x47}, 188)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rootPath, filepath.FromSlash(plan.FinalPath))
	before := []byte("existing")
	if err := os.WriteFile(finalPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := root.LinkFinal(plan); !errors.Is(err, ErrFinalExists) {
		t.Fatalf("err=%v", err)
	}
	after, err := os.ReadFile(finalPath)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("既存finalが変化しました: %q err=%v", after, err)
	}
}

func TestRecordingRootRejectsSymlinkAndConcurrentOwner(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(parent, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(linkRoot); err == nil {
		t.Fatal("symlinkの録画rootが受理されました")
	}
	root, err := OpenRoot(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := OpenRoot(realRoot); err == nil {
		t.Fatal("同じ録画rootの二重所有が受理されました")
	}
}

func TestInspectReportsUnsafeDirectoryWithoutFollowingIt(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "recordings")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "2026")); err != nil {
		t.Fatal(err)
	}
	plan, err := recording.NewFilePlan(time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), fileID(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := root.Inspect(plan)
	if err != nil || !observation.Unsafe || observation.Partial.Exists || observation.Final.Exists {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
}

func fileID(t *testing.T, marker byte) catalogmodel.ID {
	t.Helper()
	id, err := catalogmodel.NewIDFrom(bytes.NewReader(bytes.Repeat([]byte{marker}, 16)))
	if err != nil {
		t.Fatal(err)
	}
	return id
}
