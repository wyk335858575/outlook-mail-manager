//go:build windows

package mail

import (
	"errors"

	"golang.org/x/sys/windows"
)

func filesystemUsedPercent(path string) (int, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, nil, &total, &free); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, errors.New("filesystem reported zero capacity")
	}
	return int((total - free) * 100 / total), nil
}
