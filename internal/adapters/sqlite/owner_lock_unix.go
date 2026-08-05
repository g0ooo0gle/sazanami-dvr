//go:build unix

package sqlite

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const ownerLockFilename = "sazanami-dvr.lock"

func acquireOwnerLock(dataRoot string) (*os.File, error) {
	if err := validateDataRootOnly(dataRoot); err != nil {
		return nil, err
	}
	path := filepath.Join(dataRoot, ownerLockFilename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("sqlite: create owner lock")
	}
	info, err := file.Stat()
	if err != nil || !ownerOnlyRegular(info) {
		_ = file.Close()
		return nil, errors.New("sqlite: invalid owner lock")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("sqlite: data root already has an owner")
	}
	return file, nil
}

func ownerOnlyRegular(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 &&
		stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}

func ownerOnlyDirectory(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && info.Mode().Perm() == 0o700 && stat.Uid == uint32(os.Geteuid())
}

func availableFilesystemBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil || stat.Bsize <= 0 {
		return 0, errors.New("sqlite: inspect filesystem capacity")
	}
	blockSize := uint64(stat.Bsize)
	available := uint64(stat.Bavail)
	if available > ^uint64(0)/blockSize {
		return 0, errors.New("sqlite: filesystem capacity overflow")
	}
	return available * blockSize, nil
}

func releaseOwnerLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
