package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWindowsClientCommands(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"enroll", "status", "targets", "config", "ls", "cat", "stat", "grep", "write", "edit", "exec", "put", "get", "cd", "pwd", "caps", "ai-help"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == root {
			t.Errorf("missing CLI command %q: %v", name, err)
		}
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"daemon"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "CLI client only") {
		t.Fatalf("daemon must be unsupported: %v", err)
	}
}
