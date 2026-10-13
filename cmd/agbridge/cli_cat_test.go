//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestCatLiveAgainstFixture writes a small text file in the daemon's
// sandbox and asserts `agbridge cat` prints its contents verbatim on
// stdout (no JSON envelope by default).
func TestCatLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "hello.txt")
	body := []byte("hello phase2 cat\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newCatCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runCat(cmd, path, 0, 0, opts); err != nil {
		t.Fatalf("cat: %v", err)
	}
	if got := out.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("content: got %q want %q", got, body)
	}
}

// TestCatResolvesRelativeToCwd verifies a relative path arg resolves
// against the CLI's bootstrapped working directory rather than being
// sent to the daemon verbatim.
func TestCatResolvesRelativeToCwd(t *testing.T) {
	tmpDir := t.TempDir()
	body := []byte("hello relative cat\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "rel.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	dir := writeFixtureState(t, env)
	g.stateDir = dir
	defer func() { g.stateDir = "" }()
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newCatCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runCat(cmd, "rel.txt", 0, 0, opts); err != nil {
		t.Fatalf("cat: %v", err)
	}
	if got := out.Bytes(); !bytes.Equal(got, body) {
		t.Errorf("content: got %q want %q", got, body)
	}
}
