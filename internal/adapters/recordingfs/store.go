//go:build unix

// Package recordingfsは録画保存先での部分ファイル作成と、上書きしない完成処理を担当する。
package recordingfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const lockFilename = ".sazanami-dvr.lock"

var (
	// ErrPartialExistsは同じ録画処理の部分ファイル名が既に使われていることを表す。
	ErrPartialExists = errors.New("recordingfs: partial file already exists")
	// ErrFinalExistsは完成ファイル名が既に使われており、上書きしなかったことを表す。
	ErrFinalExists = recording.ErrFinalExists
)

// Rootは一つのプロセスが所有する録画保存先である。
// DBに保存した相対パスだけを受け取り、保存先の外を指すパスやシンボリックリンクを拒否する。
type Root struct {
	path string
	lock *os.File
}

// OpenRootは所有者専用の録画保存先を用意し、別プロセスとの同時利用を防ぐロックを取得する。
func OpenRoot(path string) (*Root, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("recordingfs: recording root must be an absolute canonical path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, errors.New("recordingfs: create recording root")
	}
	if err := validateDirectory(path); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(path, lockFilename)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("recordingfs: create owner lock")
	}
	if err := validateRegularFile(lock, 1); err != nil {
		_ = lock.Close()
		return nil, errors.New("recordingfs: invalid owner lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("recordingfs: recording root already has an owner")
	}
	return &Root{path: path, lock: lock}, nil
}

// Closeは録画保存先の所有ロックを解放する。
func (root *Root) Close() error {
	if root == nil || root.lock == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(root.lock.Fd()), syscall.LOCK_UN)
	closeErr := root.lock.Close()
	root.lock = nil
	return errors.Join(unlockErr, closeErr)
}

// PartialFileは作成済みの部分ファイルを、非公開の絶対パスを漏らさず操作する。
type PartialFile struct {
	file *os.File
}

// Writeは受信したストリームの一部を部分ファイルへ書く。
func (file *PartialFile) Write(data []byte) (int, error) {
	if file == nil || file.file == nil || len(data) == 0 {
		return 0, errors.New("recordingfs: invalid partial write")
	}
	written, err := file.file.Write(data)
	if err != nil {
		return written, errors.New("recordingfs: write partial file")
	}
	return written, nil
}

// Syncはファイルを閉じる前に、部分ファイルの内容を永続化する。
func (file *PartialFile) Sync() error {
	if file == nil || file.file == nil {
		return errors.New("recordingfs: invalid partial sync")
	}
	if err := file.file.Sync(); err != nil {
		return errors.New("recordingfs: sync partial file")
	}
	return nil
}

// Closeは部分ファイルを閉じる。同じ値への二回目以降の呼出しは何もしない。
func (file *PartialFile) Close() error {
	if file == nil || file.file == nil {
		return nil
	}
	err := file.file.Close()
	file.file = nil
	if err != nil {
		return errors.New("recordingfs: close partial file")
	}
	return nil
}

// CreatePartialは必要な年月ディレクトリを作り、部分ファイルを0600で排他的に作る。
func (root *Root) CreatePartial(plan recording.FilePlan) (*PartialFile, error) {
	partial, _, directory, err := root.paths(plan)
	if err != nil {
		return nil, err
	}
	if err := root.ensureDirectory(directory); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrPartialExists
	}
	if err != nil {
		return nil, errors.New("recordingfs: create partial file")
	}
	if err := validateRegularFile(file, 1); err != nil {
		_ = file.Close()
		return nil, errors.New("recordingfs: invalid new partial file")
	}
	return &PartialFile{file: file}, nil
}

// LinkFinalは部分ファイルと同じinodeを完成名で公開し、既存の完成ファイルを置き換えない。
func (root *Root) LinkFinal(plan recording.FilePlan) error {
	partial, final, _, err := root.paths(plan)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(final); err == nil {
		return ErrFinalExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("recordingfs: inspect final file")
	}
	partialInfo, err := os.Lstat(partial)
	if err != nil || !validRegularInfo(partialInfo, 1) {
		return errors.New("recordingfs: invalid partial before publication")
	}
	if err := os.Link(partial, final); errors.Is(err, os.ErrExist) {
		return ErrFinalExists
	} else if err != nil {
		return errors.New("recordingfs: publish final file")
	}
	finalInfo, finalErr := os.Lstat(final)
	partialInfo, partialErr := os.Lstat(partial)
	if finalErr != nil || partialErr != nil || !validRegularInfo(finalInfo, 2) ||
		!validRegularInfo(partialInfo, 2) || !os.SameFile(partialInfo, finalInfo) {
		return errors.New("recordingfs: final publication readback failed")
	}
	return nil
}

// SyncDirectoryは録画ファイル名を含む年月ディレクトリを永続化する。
func (root *Root) SyncDirectory(plan recording.FilePlan) error {
	_, _, directory, err := root.paths(plan)
	if err != nil {
		return err
	}
	if err := validateDirectory(directory); err != nil {
		return err
	}
	file, err := os.Open(directory)
	if err != nil {
		return errors.New("recordingfs: open recording directory")
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if syncErr != nil || closeErr != nil {
		return errors.New("recordingfs: sync recording directory")
	}
	return nil
}

// RemovePartialは完成名と同じinodeであることを確認してから部分名だけを除く。
func (root *Root) RemovePartial(plan recording.FilePlan) error {
	partial, final, _, err := root.paths(plan)
	if err != nil {
		return err
	}
	partialInfo, partialErr := os.Lstat(partial)
	finalInfo, finalErr := os.Lstat(final)
	if partialErr != nil || finalErr != nil || !validRegularInfo(partialInfo, 2) ||
		!validRegularInfo(finalInfo, 2) || !os.SameFile(partialInfo, finalInfo) {
		return errors.New("recordingfs: partial and final files do not match")
	}
	if err := os.Remove(partial); err != nil {
		return errors.New("recordingfs: remove partial name")
	}
	return nil
}

// InspectはDBに記録した二つの相対パスだけを調べ、ファイル内容や保存先の他の名前は読まない。
func (root *Root) Inspect(plan recording.FilePlan) (recording.FileObservation, error) {
	partial, final, directory, err := root.paths(plan)
	if err != nil {
		return recording.FileObservation{}, err
	}
	safe, exists, err := root.inspectDirectory(directory)
	if err != nil {
		return recording.FileObservation{}, err
	}
	if !safe {
		return recording.FileObservation{Unsafe: true}, nil
	}
	if !exists {
		return recording.FileObservation{}, nil
	}
	partialFact, partialInfo, err := inspectFile(partial)
	if err != nil {
		return recording.FileObservation{}, err
	}
	finalFact, finalInfo, err := inspectFile(final)
	if err != nil {
		return recording.FileObservation{}, err
	}
	observation := recording.FileObservation{Partial: partialFact, Final: finalFact}
	if partialInfo != nil && finalInfo != nil {
		observation.SameFile = os.SameFile(partialInfo, finalInfo)
	}
	return observation, nil
}

func (root *Root) paths(plan recording.FilePlan) (string, string, string, error) {
	if root == nil || root.path == "" || plan.Validate() != nil {
		return "", "", "", errors.New("recordingfs: invalid file plan")
	}
	partial := filepath.Join(root.path, filepath.FromSlash(plan.PartialPath))
	final := filepath.Join(root.path, filepath.FromSlash(plan.FinalPath))
	directory := filepath.Dir(partial)
	if !strings.HasPrefix(partial, root.path+string(filepath.Separator)) ||
		!strings.HasPrefix(final, root.path+string(filepath.Separator)) || directory != filepath.Dir(final) {
		return "", "", "", errors.New("recordingfs: file plan escapes recording root")
	}
	return partial, final, directory, nil
}

func (root *Root) ensureDirectory(path string) error {
	if path == root.path {
		return validateDirectory(path)
	}
	parent := filepath.Dir(path)
	if parent != root.path {
		if err := root.ensureDirectory(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("recordingfs: create recording directory")
	}
	return validateDirectory(path)
}

func (root *Root) inspectDirectory(directory string) (bool, bool, error) {
	if root == nil || root.path == "" || !strings.HasPrefix(directory, root.path+string(filepath.Separator)) {
		return false, false, errors.New("recordingfs: invalid inspection directory")
	}
	if err := validateDirectory(root.path); err != nil {
		return false, false, err
	}
	relative, err := filepath.Rel(root.path, directory)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return false, false, errors.New("recordingfs: invalid inspection directory")
	}
	current := root.path
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return true, false, nil
		}
		if err != nil {
			return false, false, errors.New("recordingfs: inspect recording directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 ||
			!ok || stat.Uid != uint32(os.Geteuid()) {
			return false, true, nil
		}
	}
	return true, true, nil
}

func inspectFile(path string) (recording.FileFact, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return recording.FileFact{}, nil, nil
	}
	if err != nil {
		return recording.FileFact{}, nil, errors.New("recordingfs: inspect recording file")
	}
	fact := recording.FileFact{Exists: true, Size: info.Size()}
	stat, ok := info.Sys().(*syscall.Stat_t)
	fact.Regular = info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Size() >= 0 && ok &&
		stat.Uid == uint32(os.Geteuid()) && (stat.Nlink == 1 || stat.Nlink == 2)
	return fact, info, nil
}

func validateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("recordingfs: invalid owner-only directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("recordingfs: recording directory has a different owner")
	}
	return nil
}

func validateRegularFile(file *os.File, links uint64) error {
	info, err := file.Stat()
	if err != nil || !validRegularInfo(info, links) {
		return errors.New("recordingfs: invalid owner-only regular file")
	}
	return nil
}

func validRegularInfo(info os.FileInfo, links uint64) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && uint64(stat.Nlink) == links
}
