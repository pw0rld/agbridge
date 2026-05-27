package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestWriteLiveAgainstFixture exercises the full WriteFileHandler
// round-trip against the in-process fake daemon. Verifies the bytes
// land on disk byte-for-byte and the printed sha256 matches.
func TestWriteLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "out.txt")
	body := []byte("hello phase2 write\n")

	env := setupPhase4(t, &config.DaemonConfig{
		AllowedWritePaths: []string{tmpDir, tmpDir + "/*"},
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newWriteCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runWrite(cmd, dst, body, 0, opts); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("file content mismatch: got %q want %q", got, body)
	}

	wantSha := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(wantSha[:])
	if !strings.Contains(out.String(), wantSHA) {
		t.Errorf("output missing sha256 %q:\n%s", wantSHA, out.String())
	}
}

// TestWriteEmptyContent verifies the touch-like semantics: passing
// zero bytes produces a zero-byte file on the daemon. This mirrors the
// existing handler-level TestSmokeF9WriteFileEmptyContent so a
// regression at either layer is caught.
func TestWriteEmptyContent(t *testing.T) {
	tmpDir := t.TempDir()
	dst := filepath.Join(tmpDir, "empty.flag")

	env := setupPhase4(t, &config.DaemonConfig{
		AllowedWritePaths: []string{tmpDir, tmpDir + "/*"},
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newWriteCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runWrite(cmd, dst, []byte{}, 0, opts); err != nil {
		t.Fatalf("write empty: %v", err)
	}

	st, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() != 0 {
		t.Errorf("size: got %d want 0", st.Size())
	}
}

// TestResolveWriteContentSources exercises the priority order of
// content sources without touching the daemon. Locks down stdin
// fallback, --content (including empty), --from-file, and the
// mutually-exclusive guard.
func TestResolveWriteContentSources(t *testing.T) {
	// 1. --content "inline" wins.
	cmd := newWriteCmd()
	cmd.SetIn(strings.NewReader("from-stdin"))
	got, err := resolveWriteContent(cmd, "inline", true, "")
	if err != nil {
		t.Fatalf("inline: %v", err)
	}
	if string(got) != "inline" {
		t.Errorf("inline: got %q", got)
	}

	// 2. --content "" is an explicit zero-byte write, NOT a fallback
	// to stdin.
	cmd2 := newWriteCmd()
	cmd2.SetIn(strings.NewReader("from-stdin"))
	got, err = resolveWriteContent(cmd2, "", true, "")
	if err != nil {
		t.Fatalf("empty content: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty content: got %q want []", got)
	}

	// 3. --from-file reads from local disk.
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "src")
	if err := os.WriteFile(srcPath, []byte("from-file"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd3 := newWriteCmd()
	got, err = resolveWriteContent(cmd3, "", false, srcPath)
	if err != nil {
		t.Fatalf("from-file: %v", err)
	}
	if string(got) != "from-file" {
		t.Errorf("from-file: got %q", got)
	}

	// 4. Default: stdin.
	cmd4 := newWriteCmd()
	cmd4.SetIn(strings.NewReader("from-stdin"))
	got, err = resolveWriteContent(cmd4, "", false, "")
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	if string(got) != "from-stdin" {
		t.Errorf("stdin: got %q", got)
	}

	// 5. --content + --from-file together fails loudly.
	cmd5 := newWriteCmd()
	if _, err := resolveWriteContent(cmd5, "a", true, "/tmp/x"); err == nil {
		t.Errorf("expected error when both flags set")
	}
}
