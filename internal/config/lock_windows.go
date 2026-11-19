package config

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile blocks until this process holds the file exclusively.
func lockFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}
