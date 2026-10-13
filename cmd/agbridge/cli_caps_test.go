//go:build !windows

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestCapsLiveAgainstFixture exercises the full CapabilitiesHandler
// round-trip against the in-process fake daemon. runFakeDaemon reuses
// daemonState.dispatch so FrameTypeCapabilitiesRequest is handled
// the same way as in the real binary.
func TestCapsLiveAgainstFixture(t *testing.T) {
	env := setupPhase4(t, &config.DaemonConfig{
		DaemonName:        "lab01",
		DefaultWorkingDir: "/tmp",
		EnvAllowlist:      []string{"PATH", "HOME"},
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newCapsCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runCaps(cmd, opts); err != nil {
		t.Fatalf("caps: %v", err)
	}
	got := out.String()
	for _, want := range []string{"daemon_name:", "lab01", "default_working_dir", "/tmp"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
