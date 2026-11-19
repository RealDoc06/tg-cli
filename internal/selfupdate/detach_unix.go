//go:build !windows

package selfupdate

import (
	"os/exec"
	"syscall"
)

// detach makes cmd outlive this process and its terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
