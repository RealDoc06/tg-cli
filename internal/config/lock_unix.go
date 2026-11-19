//go:build !windows

package config

import (
	"os"
	"syscall"
)

// lockFile blocks until this process holds the file exclusively.
func lockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
