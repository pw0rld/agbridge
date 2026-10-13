//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestGrepLiveAgainstFixture writes two matching files and one
// non-matching file in the daemon's sandbox, then asserts the literal
// `agbridge grep` output contains hits for the matching paths but not
// the non-matching one. Exercises the full SearchHandler round-trip
// through the in-process fake daemon (handleSearch dispatches via
// daemonState.dispatch, same as the real binary).
func TestGrepLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hello world\nneedle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("another needle line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "c.txt"), []byte("no match here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newGrepCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runGrep(cmd, "needle", tmpDir, "", false, 0, opts); err != nil {
		t.Fatalf("grep: %v", err)
	}
	got := out.String()
	for _, want := range []string{"a.txt", "b.txt", "needle"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "c.txt") {
		t.Errorf("c.txt should not match:\n%s", got)
	}
}

// TestGrepIgnoreCase verifies the --ignore-case flag flows through to
// the daemon. The pattern is upper-case; the file content is lower-case.
func TestGrepIgnoreCase(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "x.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	// Sanity: without --ignore-case, "NEEDLE" should not match
	// lower-case "needle" (regex mode, default).
	var out1 bytes.Buffer
	cmd1 := newGrepCmd()
	cmd1.SetOut(&out1)
	cmd1.SetContext(env.ctx)
	if err := runGrep(cmd1, "NEEDLE", tmpDir, "", false, 0, opts); err != nil {
		t.Fatalf("grep case-sensitive: %v", err)
	}
	if strings.Contains(out1.String(), "x.txt") {
		t.Errorf("case-sensitive match should not hit:\n%s", out1.String())
	}

	// With --ignore-case, the same pattern hits.
	var out2 bytes.Buffer
	cmd2 := newGrepCmd()
	cmd2.SetOut(&out2)
	cmd2.SetContext(env.ctx)
	if err := runGrep(cmd2, "NEEDLE", tmpDir, "", true, 0, opts); err != nil {
		t.Fatalf("grep ignore-case: %v", err)
	}
	if !strings.Contains(out2.String(), "x.txt") {
		t.Errorf("ignore-case match should hit:\n%s", out2.String())
	}
}
