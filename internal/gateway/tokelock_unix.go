//go:build unix

package gateway

import (
	"errors"
	"os"
	"syscall"
)

// tryLockTokenFile attempts a non-blocking exclusive flock. Reports
// (false, nil) when another process holds the lock.
func tryLockTokenFile(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func unlockTokenFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
