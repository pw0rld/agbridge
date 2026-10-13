//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestExecLiveAgainstFixture exercises the full ExecHandler round-trip
// against the in-process fake daemon, mirroring the bellwether
// TestCLIInvokeExecAgainstFixture but driving runExec() so the cobra
// flag plumbing is also covered.
func TestExecLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		ExecEnabled:  true,
		EnvAllowlist: []string{"PATH"},
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := newExecCmd()
	cmd.SetOut(&stdoutBuf)
	cmd.SetErr(&stderrBuf)
	cmd.SetContext(env.ctx)

	if err := runExec(cmd, []string{"/bin/echo", "ok"}, tmpDir, nil, 5000, opts); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(stdoutBuf.String(), "ok") {
		t.Errorf("stdout missing %q: %q", "ok", stdoutBuf.String())
	}
}

// TestExecJSONShape verifies the --json envelope: stdout, stderr,
// exitcode, duration_ms, _schema. Locks down the shape so AI callers
// can parse it without sniffing for absent fields.
func TestExecJSONShape(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		ExecEnabled:  true,
		EnvAllowlist: []string{"PATH"},
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	g.json = true
	defer func() {
		g.timeout = 0
		g.json = false
	}()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := newExecCmd()
	cmd.SetOut(&stdoutBuf)
	cmd.SetErr(&stderrBuf)
	cmd.SetContext(env.ctx)

	if err := runExec(cmd, []string{"/bin/echo", "ok"}, tmpDir, nil, 5000, opts); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(stdoutBuf.Bytes(), &got); err != nil {
		t.Fatalf("json: %v: %s", err, stdoutBuf.String())
	}
	if _, ok := got["stdout"]; !ok {
		t.Errorf("missing stdout in JSON: %v", got)
	}
	if _, ok := got["stderr"]; !ok {
		t.Errorf("missing stderr in JSON: %v", got)
	}
	if _, ok := got["exitcode"]; !ok {
		t.Errorf("missing exitcode in JSON: %v", got)
	}
	if _, ok := got["duration_ms"]; !ok {
		t.Errorf("missing duration_ms in JSON: %v", got)
	}
	if v, _ := got["exitcode"].(float64); v != 0 {
		t.Errorf("exitcode: got %v want 0", v)
	}
	if sout, _ := got["stdout"].(string); !strings.Contains(sout, "ok") {
		t.Errorf("stdout missing %q: %q", "ok", sout)
	}
}

// TestExecDefaultsCwdToWorkingDir verifies that omitting --cwd falls
// back to the CLI's bootstrapped working directory instead of leaving
// cwd empty on the wire.
func TestExecDefaultsCwdToWorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		ExecEnabled:       true,
		EnvAllowlist:      []string{"PATH"},
		DefaultWorkingDir: tmpDir,
	})
	dir := writeFixtureState(t, env)
	g.stateDir = dir
	defer func() { g.stateDir = "" }()
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := newExecCmd()
	cmd.SetOut(&stdoutBuf)
	cmd.SetErr(&stderrBuf)
	cmd.SetContext(env.ctx)

	if err := runExec(cmd, []string{"/bin/pwd"}, "", nil, 5000, opts); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(stdoutBuf.String(), tmpDir) {
		t.Errorf("stdout missing cwd %q: %q", tmpDir, stdoutBuf.String())
	}
}

// TestSplitExecArgsRequiresCommand asserts the helper rejects empty
// remote-cmd input. The cobra MinimumNArgs(1) check fires for the
// fully-empty case, but splitExecArgs guards the
// `agbridge exec --` (dash present, no trailing args) corner.
func TestSplitExecArgsRequiresCommand(t *testing.T) {
	cmd := newExecCmd()
	// Simulate `agbridge exec --` by running ParseFlags with bare `--`.
	if err := cmd.ParseFlags([]string{"--"}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if _, err := splitExecArgs(cmd, []string{}); err == nil {
		t.Errorf("expected error when no remote command supplied")
	}
}

// TestParseEnvPairs locks down the KEY=VALUE parsing rules: empty
// value is OK, bare KEY is rejected.
func TestParseEnvPairs(t *testing.T) {
	out, err := parseEnvPairs([]string{"FOO=bar", "EMPTY="})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out["FOO"] != "bar" {
		t.Errorf("FOO: got %q", out["FOO"])
	}
	if v, ok := out["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY: got %q ok=%v", v, ok)
	}
	if _, err := parseEnvPairs([]string{"BAREKEY"}); err == nil {
		t.Errorf("expected error for bare key")
	}
}
