package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/execproto"
)

func TestExecEchoSuccess(t *testing.T) {
	tmp := t.TempDir()
	req := execproto.ExecRequest{
		Cmd:       "/bin/sh",
		Args:      []string{"-c", "echo hello && echo bad >&2"},
		Cwd:       tmp,
		TimeoutMs: 5000,
	}
	var stdout, stderr strings.Builder
	collect := func(c execproto.ExecChunk) {
		switch c.Stream {
		case "stdout":
			stdout.Write(c.Data)
		case "stderr":
			stderr.Write(c.Data)
		}
	}
	complete, err := Exec(context.Background(), req, []string{tmp + "/*", tmp}, []string{"PATH"}, collect)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if complete.ExitCode != 0 {
		t.Errorf("exit code: %d", complete.ExitCode)
	}
	if !strings.Contains(stdout.String(), "hello") {
		t.Errorf("stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bad") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

func TestExecCwdForbidden(t *testing.T) {
	req := execproto.ExecRequest{
		Cmd:       "/bin/true",
		Cwd:       "/etc",
		TimeoutMs: 1000,
	}
	_, err := Exec(context.Background(), req, []string{"/home/user/*"}, nil, func(execproto.ExecChunk) {})
	if err == nil {
		t.Errorf("expected cwd error")
	}
}

func TestExecTimeoutKills(t *testing.T) {
	tmp := t.TempDir()
	req := execproto.ExecRequest{
		Cmd:       "/bin/sh",
		Args:      []string{"-c", "sleep 5"},
		Cwd:       tmp,
		TimeoutMs: 200,
	}
	start := time.Now()
	complete, err := Exec(context.Background(), req, []string{tmp + "/*", tmp}, []string{"PATH"}, func(execproto.ExecChunk) {})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !complete.TimedOut {
		t.Errorf("expected TimedOut=true")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("did not kill in reasonable time: %v", time.Since(start))
	}
}

func TestExecEnvWhitelist(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SECRET", "leaked")
	req := execproto.ExecRequest{
		Cmd:       "/bin/sh",
		Args:      []string{"-c", "echo SECRET=$SECRET"},
		Cwd:       tmp,
		TimeoutMs: 5000,
	}
	var out strings.Builder
	_, err := Exec(context.Background(), req, []string{tmp + "/*", tmp}, []string{"PATH"}, func(c execproto.ExecChunk) {
		if c.Stream == "stdout" {
			out.Write(c.Data)
		}
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out.String(), "leaked") {
		t.Errorf("env whitelist failed: %q", out.String())
	}
}
