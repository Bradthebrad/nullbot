//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// secretProcess runs a secret_run command in a job object that kills every
// process in it when closed, so nothing it starts outlives the call holding
// the secrets.
type secretProcess struct {
	cmd *exec.Cmd
	job windows.Handle
}

func newSecretProcess(cmd *exec.Cmd) *secretProcess {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	p := &secretProcess{cmd: cmd}
	cmd.Cancel = func() error { p.killAll(); return nil }
	return p
}

var procNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// start creates the process suspended, puts it in the job, and only then
// lets it run, so nothing it starts can slip out before the job holds it.
func (p *secretProcess) start() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err == nil {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
			p.job = job
		} else {
			windows.CloseHandle(job)
		}
	}
	suspended := p.job != 0 && procNtResumeProcess.Find() == nil
	if suspended {
		p.cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	}
	if err := p.cmd.Start(); err != nil {
		p.finish()
		return err
	}
	if p.job == 0 {
		return nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(p.cmd.Process.Pid))
	if err != nil {
		if suspended {
			// It cannot be put in the job or resumed: don't leave it hanging.
			_ = p.cmd.Process.Kill()
			p.finish()
			return err
		}
		return nil
	}
	defer windows.CloseHandle(h)
	_ = windows.AssignProcessToJobObject(p.job, h)
	if suspended {
		if status, _, _ := procNtResumeProcess.Call(uintptr(h)); status != 0 {
			_ = p.cmd.Process.Kill()
			p.finish()
			return fmt.Errorf("could not start the command (NTSTATUS 0x%x)", status)
		}
	}
	return nil
}

func (p *secretProcess) killAll() {
	if p.job != 0 {
		_ = windows.TerminateJobObject(p.job, 1)
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// finish closes the job, which ends whatever the command left running.
func (p *secretProcess) finish() {
	if p.job != 0 {
		windows.CloseHandle(p.job)
		p.job = 0
	}
}

// shellCommand runs command through cmd.exe exactly as written: the
// command line is passed whole, so quotes inside it survive.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	comspec := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	if os.Getenv("SystemRoot") == "" {
		comspec = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + command + `"`}
	return cmd
}
