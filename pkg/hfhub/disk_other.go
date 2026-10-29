//go:build !linux && !darwin && !freebsd && !windows

package hfhub

// freeSpace cannot be measured portably on this system, so the check before
// a download is skipped; a full disk still fails the write.
func freeSpace(string) (uint64, bool) { return 0, false }

func isDiskFull(error) bool { return false }
