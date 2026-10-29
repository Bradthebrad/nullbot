//go:build !windows

package llmfit

import "os/exec"

func hideWindow(*exec.Cmd) {}
