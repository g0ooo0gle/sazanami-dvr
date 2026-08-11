//go:build unix

package recordingfs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
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

func TestOpenFinalChecksPublishedFileAndExpectedSize(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "recordings")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan, err := recording.NewFilePlan(time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), fileID(t, 9))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := root.CreatePartial(plan)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0x47}, 188)
	if _, err := partial.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.LinkFinal(plan); err != nil {
		t.Fatal(err)
	}
	if err := root.RemovePartial(plan); err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFinal(plan, 188)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("bytes=%d err=%v", len(got), err)
	}
	fd := file.file.Fd()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(fd), &stat); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("完成fileのdescriptorが閉じていません: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.OpenFinal(plan, 189); err == nil {
		t.Fatal("size mismatch accepted")
	}
	finalPath := filepath.Join(rootPath, filepath.FromSlash(plan.FinalPath))
	linkPath := finalPath + ".link"
	if err := os.Link(finalPath, linkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := root.OpenFinal(plan, 188); err == nil {
		t.Fatal("hard linkを持つ完成fileが受理されました")
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(finalPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := root.OpenFinal(plan, 188); err == nil {
		t.Fatal("unsafe mode accepted")
	}
}

func TestValidRegularInfoRejectsDifferentOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.ts")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x47}, 188), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid++
	if validRegularInfo(fileInfoWithStat{FileInfo: info, stat: &stat}, 1) {
		t.Fatal("所有者が異なる完成file情報が受理されました")
	}
}

func TestOpenFinalRejectsSymlinkAndDetectsReplacementAfterOpen(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "recordings")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plan, err := recording.NewFilePlan(time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), fileID(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	partial, err := root.CreatePartial(plan)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0x47}, 188)
	if _, err := partial.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}
	if err := root.LinkFinal(plan); err != nil {
		t.Fatal(err)
	}
	if err := root.RemovePartial(plan); err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(rootPath, filepath.FromSlash(plan.FinalPath))
	opened, err := root.OpenFinal(plan, 188)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := finalPath + ".old"
	if err := os.Rename(finalPath, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.Read(make([]byte, 1)); err == nil {
		t.Fatal("open後のfile差替えが検出されませんでした")
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(finalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldPath, finalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := root.OpenFinal(plan, 188); err == nil {
		t.Fatal("symlinkの完成fileが受理されました")
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

type fileInfoWithStat struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (info fileInfoWithStat) Sys() any { return info.stat }
