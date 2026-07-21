package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestPwdBootstrapsFromCaps verifies that pwd, run against a fresh bridge
// state with no WorkingDir set, bootstraps from the daemon's advertised
// default_working_dir (via capabilities) and persists + prints it.
func TestPwdBootstrapsFromCaps(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	dir := writeFixtureState(t, env)
	prevStateDir := g.stateDir
	g.stateDir = dir
	defer func() { g.stateDir = prevStateDir }()

	var out bytes.Buffer
	cmd := newPwdCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runPwd(cmd, opts); err != nil {
		t.Fatalf("pwd: %v", err)
	}
	want := tmpDir + "\n"
	if got := out.String(); got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

// TestCdValidatesDirAndPersists verifies that cd stat-validates the target
// directory against the daemon and persists it to state, so a subsequent
// pwd reflects the new working directory.
func TestCdValidatesDirAndPersists(t *testing.T) {
	tmpDir := t.TempDir()
	sub := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	dir := writeFixtureState(t, env)
	prevStateDir := g.stateDir
	g.stateDir = dir
	defer func() { g.stateDir = prevStateDir }()

	cdCmd := newCdCmd()
	cdCmd.SetOut(&bytes.Buffer{})
	cdCmd.SetContext(env.ctx)
	if err := runCd(cdCmd, "sub", opts); err != nil {
		t.Fatalf("cd sub: %v", err)
	}

	var out bytes.Buffer
	pwdCmd := newPwdCmd()
	pwdCmd.SetOut(&out)
	pwdCmd.SetContext(env.ctx)
	if err := runPwd(pwdCmd, opts); err != nil {
		t.Fatalf("pwd: %v", err)
	}
	want := sub + "\n"
	if got := out.String(); got != want {
		t.Fatalf("pwd after cd = %q, want %q", got, want)
	}
}

// TestCdRejectsMissingDir verifies that cd fails when the target doesn't
// exist on the daemon (stat fails).
func TestCdRejectsMissingDir(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	dir := writeFixtureState(t, env)
	prevStateDir := g.stateDir
	g.stateDir = dir
	defer func() { g.stateDir = prevStateDir }()

	cmd := newCdCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetContext(env.ctx)
	if err := runCd(cmd, "does-not-exist", opts); err == nil {
		t.Fatal("cd to missing dir should error")
	}
}
