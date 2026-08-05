//go:build linux

package sqlite

import (
	"errors"
	"syscall"
)

func validateLocalFilesystem(path string) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return errors.New("sqlite: inspect filesystem type")
	}
	allowed := map[int64]struct{}{
		0xEF53: {}, 0x58465342: {}, 0x9123683E: {}, 0x01021994: {}, 0x794C7630: {}, 0x2FC12FC1: {},
	}
	if _, ok := allowed[stat.Type]; !ok {
		return errors.New("sqlite: filesystem is not an accepted local type")
	}
	return nil
}
