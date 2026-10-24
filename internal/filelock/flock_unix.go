//go:build unix

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock attempts a non-blocking exclusive flock. Reports
// (false, nil) when another process holds the lock. dataPath is unused
// here: flock is already cross-process on unix.
func tryLock(_ string, f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func unlock(_ string, f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
