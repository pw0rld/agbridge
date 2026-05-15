package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendOneEvent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	w, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	if err := w.Append(map[string]any{"event": "started", "tool": "exec", "req_id": "r1"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	line := strings.TrimSpace(string(b))
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["tool"] != "exec" || got["req_id"] != "r1" {
		t.Errorf("got %+v", got)
	}
}

func TestAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	w, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	var wg sync.WaitGroup
	const n = 200
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = w.Append(map[string]any{"i": i})
		}(i)
	}
	wg.Wait()

	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	count := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal([]byte(sc.Text()), &m); err != nil {
			t.Errorf("malformed line: %v", err)
		}
		count++
	}
	if count != n {
		t.Errorf("got %d lines, want %d", count, n)
	}
}

func TestRotationOnSizeExceeded(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	w, err := OpenWith(p, Options{MaxBytes: 50, MaxBackups: 3})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	// Each {"i":N,"ts":"fixed"}\n is ~21 bytes; 3 lines push past 50 bytes.
	for i := 0; i < 6; i++ {
		if err := w.Append(map[string]any{"i": i, "ts": "fixed"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf(".1 not present: %v", err)
	}
	active, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	if int64(len(active)) > 100 {
		t.Errorf("active file %d bytes, much larger than expected", len(active))
	}
}

func TestRotationRespectsMaxBackups(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	w, err := OpenWith(p, Options{MaxBytes: 50, MaxBackups: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	// Write enough to force >2 rotations.
	for i := 0; i < 20; i++ {
		if err := w.Append(map[string]any{"i": i, "ts": "fixed"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	for _, suffix := range []string{".1", ".2"} {
		if _, err := os.Stat(p + suffix); err != nil {
			t.Errorf("expected %s, got %v", p+suffix, err)
		}
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Errorf(".3 should not exist with MaxBackups=2")
	}
}

func TestNoRotationWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	w, err := Open(p) // no rotation
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()
	for i := 0; i < 50; i++ {
		_ = w.Append(map[string]any{"i": i, "ts": "fixed"})
	}
	if _, err := os.Stat(p + ".1"); err == nil {
		t.Errorf(".1 created but rotation was disabled")
	}
}

func TestRotationSizeAccountingAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.jsonl")
	// Pre-seed file close to MaxBytes.
	w1, err := OpenWith(p, Options{MaxBytes: 50, MaxBackups: 1})
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	_ = w1.Append(map[string]any{"x": "padding-data-of-some-bytes", "ts": "fixed"})
	w1.Close()

	w2, err := OpenWith(p, Options{MaxBytes: 50, MaxBackups: 1})
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer w2.Close()
	// Next write should push past MaxBytes and trigger rotation.
	_ = w2.Append(map[string]any{"x": "another-line-that-should-cause-rotation", "ts": "fixed"})
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("expected rotation on second open, .1 missing: %v", err)
	}
}
