//go:build !windows

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockPath takes an exclusive lock on path, waiting up to two minutes for
// another NullBot process (a keychain prompt can hold one that long).
func lockPath(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, errBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func renameReplace(from, to string) error { return os.Rename(from, to) }

// syncDir makes a rename durable before anything depends on it.
func syncDir(dir string) {
	if d, err := os.Open(filepath.Clean(dir)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
