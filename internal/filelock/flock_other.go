//go:build !unix

package filelock

import (
	"os"
	"sync"
)

// No cross-process advisory lock is available on this platform (the
// gateway is unix-only; Windows ships the CLI, whose `cd` and
// `config use` still need load→modify→save serialisation). Fall back to
// a process-local mutex per data path so concurrent invocations inside
// one process cannot lose updates. This is strictly weaker than flock:
// two separate processes racing on the same file are still unprotected.
var (
	localMu    sync.Mutex
	localLocks = map[string]chan struct{}{}
)

func localSem(dataPath string) chan struct{} {
	localMu.Lock()
	defer localMu.Unlock()
	sem, ok := localLocks[dataPath]
	if !ok {
		sem = make(chan struct{}, 1)
		localLocks[dataPath] = sem
	}
	return sem
}

// tryLock is a non-blocking process-local acquire; Acquire's retry loop
// turns it into the same bounded wait as the flock implementation.
func tryLock(dataPath string, _ *os.File) (bool, error) {
	select {
	case localSem(dataPath) <- struct{}{}:
		return true, nil
	default:
		return false, nil
	}
}

func unlock(dataPath string, _ *os.File) {
	select {
	case <-localSem(dataPath):
	default:
	}
}
