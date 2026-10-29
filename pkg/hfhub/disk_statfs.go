//go:build linux || darwin || freebsd

package hfhub

import (
	"errors"
	"syscall"
)

// freeSpace reports the bytes an unprivileged user may still write in dir.
// It uses Bavail rather than Bfree: blocks reserved for root do not count.
func freeSpace(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	// Bavail is signed on FreeBSD, where it goes negative once the reserve
	// is in use; the conversions below work for every field type.
	avail, bsize := int64(st.Bavail), int64(st.Bsize)
	if avail <= 0 || bsize <= 0 {
		return 0, true
	}
	return uint64(avail) * uint64(bsize), true
}

func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
