//go:build linux

package app

import (
	"os/exec"
	"syscall"
)

// endWithParent kills the command if NullBot itself dies, so a tunnel or a
// cloud command holding a key file never outlives it. Its own session
// (Setsid) would otherwise keep it running.
func endWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
