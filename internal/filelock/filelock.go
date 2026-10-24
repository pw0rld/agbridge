// Package filelock provides a cross-process advisory lock guarding a
// data file. The lock lives on a sidecar "<path>.lock" rather than the
// data file itself: the persistence layer replaces data files via
// temp+rename, so a lock held on the data inode would end up guarding
// an orphaned file after a sibling's flush.
package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultTimeout bounds how long Acquire waits before giving up, so a
// wedged holder cannot stall a sibling process indefinitely.
const DefaultTimeout = 5 * time.Second

// Locker is a held exclusive lock. The zero value is not usable;
// obtain one from Acquire.
type Locker struct {
	f    *os.File
	path string
}

// Acquire takes an exclusive cross-process lock guarding dataPath by
// flocking <dataPath>.lock, polling every 20ms until timeout. A
// timeout of 0 selects DefaultTimeout. The caller must call Release.
func Acquire(dataPath string, timeout time.Duration) (*Locker, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	dir := filepath.Dir(dataPath)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	f, err := os.OpenFile(dataPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("filelock: open lockfile: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		ok, err := tryLock(dataPath, f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("filelock: lock: %w", err)
		}
		if ok {
			return &Locker{f: f, path: dataPath}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, errors.New("filelock: timed out waiting for lock on " + dataPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Release drops the lock and closes the sidecar file. Safe on nil.
func (l *Locker) Release() {
	if l == nil {
		return
	}
	unlock(l.path, l.f)
	_ = l.f.Close()
}
