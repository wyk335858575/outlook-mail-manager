//go:build !windows

package mail

import (
	"errors"
	"syscall"
)

func filesystemUsedPercent(path string) (int, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if total == 0 {
		return 0, errors.New("filesystem reported zero capacity")
	}
	return int((total - free) * 100 / total), nil
}
