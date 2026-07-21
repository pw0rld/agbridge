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

// TestGetLiveAgainstFixture exercises the full DownloadHandler
// round-trip in dest_path streaming mode: daemon reads /tmp/src, sends
// chunks; the cliclient writes them straight to /tmp/dst. Verifies
// bytes and the printed sha256.
func TestGetLiveAgainstFixture(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "src.bin")
	dstPath := filepath.Join(dstDir, "dst.bin")
	body := []byte("hello phase2 get\n")
	if err := os.WriteFile(srcPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: srcDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newGetCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runGet(cmd, srcPath, dstPath, opts); err != nil {
		t.Fatalf("get: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("dst mismatch: got %q want %q", got, body)
	}
	want := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(want[:])
	if !strings.Contains(out.String(), wantSHA) {
		t.Errorf("output missing sha256 %q:\n%s", wantSHA, out.String())
	}
}
