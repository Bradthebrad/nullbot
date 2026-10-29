//go:build !windows

package app

import (
	"os/exec"
	"syscall"
)

// configureSecretProcess runs the command in its own process group so a
// timeout stops everything it started.
func configureSecretProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
