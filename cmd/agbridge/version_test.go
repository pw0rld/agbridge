package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootVersionFlagPrintsExactVersion(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != agbridgeVersion {
		t.Fatalf("--version = %q, want %q", got, agbridgeVersion)
	}
}
