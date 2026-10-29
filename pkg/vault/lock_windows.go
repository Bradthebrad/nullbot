//go:build windows

package vault

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockPath takes an exclusive lock on path, waiting up to two minutes for
// another NullBot process.
func lockPath(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(f.Fd())
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ol := new(windows.Overlapped)
		err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
		if err == nil {
			return func() {
				_ = windows.UnlockFileEx(h, 0, 1, 0, new(windows.Overlapped))
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) || time.Now().After(deadline) {
			f.Close()
			return nil, errBusy
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// renameReplace retries briefly: antivirus scanners and other readers can
// hold the vault open for a moment, which makes the rename fail.
func renameReplace(from, to string) error {
	var err error
	for i := 0; i < 40; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return err
}

func syncDir(string) {}
