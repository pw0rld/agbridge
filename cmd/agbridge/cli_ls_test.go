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

// TestLsLiveAgainstFixture seeds a few entries in tmpDir, configures the
// fake daemon's DefaultWorkingDir, and verifies `agbridge ls` returns
// them in the human table.
func TestLsLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	// Two files and one subdir so the table has variety.
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.log"), []byte("yo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newLsCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runLs(cmd, tmpDir, false, 0, opts); err != nil {
		t.Fatalf("ls: %v", err)
	}
	got := out.String()
	for _, want := range []string{"a.txt", "b.log", "sub"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestLsRecursive exercises the client-side BFS walk with --recursive.
// Only verifies the sub-directory is visited; deeper depth-cap behavior
// is unit-tested implicitly by the BFS queue logic.
func TestLsRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "sub", "deep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newLsCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runLs(cmd, tmpDir, true, 0, opts); err != nil {
		t.Fatalf("ls -R: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "deep.txt") {
		t.Errorf("expected to visit subdir and see deep.txt, got:\n%s", got)
	}
}

// TestLsResolvesRelativeToCwd verifies a relative `ls sub` resolves
// against the CLI's bootstrapped working directory (the fixture state
// has an empty WorkingDir, so ensureWorkingDir bootstraps it from the
// daemon's DefaultWorkingDir).
func TestLsResolvesRelativeToCwd(t *testing.T) {
	tmpDir := t.TempDir()
	sub := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("x"), 0o644); err != nil {
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
	cmd := newLsCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runLs(cmd, "sub", false, 0, opts); err != nil {
		t.Fatalf("ls sub: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "f.txt") {
		t.Errorf("output missing %q:\n%s", "f.txt", got)
	}
}
