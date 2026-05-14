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
