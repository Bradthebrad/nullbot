//go:build !windows && !linux

package app

import "os/exec"

// endWithParent has no equivalent here; signal handling ends what NullBot
// started when it is asked to stop.
func endWithParent(*exec.Cmd) {}
