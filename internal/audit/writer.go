// Package audit appends structured records to a JSONL file with one
// JSON object per line. Goroutine-safe for concurrent Append calls.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Options tunes the writer. Zero MaxBytes or MaxBackups disables rotation
// entirely (writes append indefinitely to a single file).
type Options struct {
	MaxBytes   int64 // rotate when the next write would push past this
	MaxBackups int   // number of .1 .. .N files to keep
}

// Writer appends events to a JSONL file. Safe for concurrent use.
type Writer struct {
	mu       sync.Mutex
	f        *os.File
	path     string
	size     int64
	maxBytes int64
	backups  int
}

// Open opens (or creates) the file at path for append with no rotation.
func Open(path string) (*Writer, error) {
	return OpenWith(path, Options{})
}

// OpenWith opens (or creates) the file at path. When opts.MaxBytes > 0 and
// opts.MaxBackups > 0, writes that would push the active file past MaxBytes
// trigger synchronous rotation: oldest backup is dropped, .i is shifted to
// .i+1, the active file is renamed to .1, and a fresh active file is opened.
func OpenWith(path string, opts Options) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	// Honor existing on-disk size so a process restart doesn't double-spend
	// the rotation budget.
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Writer{
		f:        f,
		path:     path,
		size:     info.Size(),
		maxBytes: opts.MaxBytes,
		backups:  opts.MaxBackups,
	}, nil
}

// Append marshals event to JSON, adds a newline, and writes it atomically.
// A "ts" field is injected if absent. When rotation is configured and the
// next write would exceed MaxBytes, the file is rotated synchronously before
// the write so callers never observe a partial line.
func (w *Writer) Append(event map[string]any) error {
	if _, ok := event["ts"]; !ok {
		event["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.maxBytes > 0 && w.backups > 0 && w.size+int64(len(b)) > w.maxBytes {
		if rerr := w.rotateLocked(); rerr != nil {
			return rerr
		}
	}
	n, err := w.f.Write(b)
	w.size += int64(n)
	return err
}

// rotateLocked closes the active file, shifts old backups, and reopens path
// fresh. Caller must hold w.mu.
func (w *Writer) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", w.path, w.backups))
	for i := w.backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	_ = os.Rename(w.path, fmt.Sprintf("%s.1", w.path))
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.size = 0
	return nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
