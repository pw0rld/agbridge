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

// TestEditLiveAgainstFixture seeds a file, runs `agbridge edit` with
// a single old→new substitution, and verifies the on-disk bytes match.
// Exercises the full EditFileHandler round-trip through the in-process
// fake daemon (handleEdit -> tools.EditFile).
func TestEditLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.go")
	original := "const Mode = \"legacy\"\nprint(\"hi\")\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newEditCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runEdit(cmd, path, `"legacy"`, `"v2"`, false, opts); err != nil {
		t.Fatalf("edit: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	wantContent := "const Mode = \"v2\"\nprint(\"hi\")\n"
	if string(got) != wantContent {
		t.Errorf("on-disk content mismatch:\ngot:  %q\nwant: %q", got, wantContent)
	}
	// Human output advertises the edited byte count + sha256.
	if !strings.Contains(out.String(), "edited") {
		t.Errorf("output missing 'edited':\n%s", out.String())
	}
}

// TestEditReplaceAll verifies the --replace-all flag flows through.
// Without it, multiple matches must error; with it, all occurrences
// are swapped in a single call.
func TestEditReplaceAll(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "many.txt")
	if err := os.WriteFile(path, []byte("foo foo foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	// Default: multiple matches must fail loudly (the daemon refuses
	// the ambiguous edit; we just need an error path that doesn't
	// touch the file).
	var out1 bytes.Buffer
	errOut1 := &bytes.Buffer{}
	cmd1 := newEditCmd()
	cmd1.SetOut(&out1)
	cmd1.SetErr(errOut1)
	cmd1.SetContext(env.ctx)
	if err := runEdit(cmd1, path, "foo", "bar", false, opts); err == nil {
		t.Errorf("expected error when old appears multiple times without --replace-all")
	}
	// File untouched.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "foo foo foo\n" {
		t.Errorf("file mutated despite error: %q", got)
	}

	// With --replace-all, every occurrence is swapped.
	var out2 bytes.Buffer
	cmd2 := newEditCmd()
	cmd2.SetOut(&out2)
	cmd2.SetContext(env.ctx)
	if err := runEdit(cmd2, path, "foo", "bar", true, opts); err != nil {
		t.Fatalf("edit --replace-all: %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "bar bar bar\n" {
		t.Errorf("replace-all result: got %q want %q", got, "bar bar bar\n")
	}
}
