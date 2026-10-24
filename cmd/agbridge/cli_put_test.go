//go:build !windows

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

// TestPutLiveAgainstFixture exercises the full UploadHandler round-trip
// in local_path mode: bridge reads /tmp/src, streams 1 MiB chunks to
// the daemon, daemon writes /tmp/dst atomically. Verifies bytes match
// and the printed sha256 matches the computed one.
func TestPutLiveAgainstFixture(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "src.bin")
	dstPath := filepath.Join(dstDir, "dst.bin")
	body := []byte("hello phase2 put\n")
	if err := os.WriteFile(srcPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: dstDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newPutCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runPut(cmd, srcPath, dstPath, 0o644, opts); err != nil {
		t.Fatalf("put: %v", err)
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

// TestParseMode covers octal parsing + empty/zero handling.
func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
		ok   bool
	}{
		{"", 0, true},
		{"0", 0, true},
		{"0644", 0o644, true},
		{"644", 0o644, true},
		{"755", 0o755, true},
		{"777", 0o777, true},
		{"0777", 0o777, true},
		{"bogus", 0, false},
		// setuid/setgid/sticky are rejected outright (daemon never honors them)
		{"7777", 0, false},
		{"07777", 0, false},
		{"4755", 0, false},
		{"1000", 0, false},
	}
	for _, c := range cases {
		got, err := parseMode(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("parseMode(%q): %v", c.in, err)
				continue
			}
			if got != c.want {
				t.Errorf("parseMode(%q): got %o want %o", c.in, got, c.want)
			}
		} else if err == nil {
			t.Errorf("parseMode(%q): expected error, got %o", c.in, got)
		}
	}
}
