package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestAIHelpPrintsCheatsheet(t *testing.T) {
	var out bytes.Buffer
	cmd := newAIHelpCmd()
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"agbridge ls",
		"agbridge exec --",
		"--json",
		"retry_safe",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("AI help missing %q in:\n%s", want, got[:min(200, len(got))])
		}
	}
}
