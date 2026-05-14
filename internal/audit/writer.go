// Package audit appends structured records to a JSONL file with one
// JSON object per line. Goroutine-safe for concurrent Append calls.
package audit

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Writer appends events to a JSONL file. Safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	f  *os.File
}

// Open opens (or creates) the file at path for append.
func Open(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f}, nil
}

// Append marshals event to JSON, adds a newline, and writes it atomically.
// A "ts" field is injected if absent.
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
	_, err = w.f.Write(b)
	return err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
