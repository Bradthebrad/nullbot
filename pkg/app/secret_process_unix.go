//go:build !windows

package app

import (
	"context"
	"os/exec"
	"syscall"
)

// secretProcess runs a secret_run command in a session of its own, so
// everything it starts, including what it leaves in the background, can be
// stopped with it: nothing outlives the call holding the secrets.
type secretProcess struct{ cmd *exec.Cmd }

func newSecretProcess(cmd *exec.Cmd) *secretProcess {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A new session also means no controlling terminal: a program that asks
	// for a password on /dev/tty fails instead of hanging.
	cmd.SysProcAttr.Setsid = true
	endWithParent(cmd)
	p := &secretProcess{cmd: cmd}
	cmd.Cancel = func() error { p.killAll(); return nil }
	return p
}

func (p *secretProcess) start() error { return p.cmd.Start() }

// finish stops whatever the command left running.
func (p *secretProcess) finish() { p.killAll() }

func (p *secretProcess) killAll() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func shellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}
