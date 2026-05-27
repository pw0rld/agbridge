package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestGlobalFlagsRegistered(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--help"})
	_ = root.Execute()
	out := buf.String()
	for _, want := range []string{"--target", "--json", "--state-dir", "--timeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help missing flag %q\noutput: %s", want, out)
		}
	}
}
