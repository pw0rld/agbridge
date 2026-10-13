//go:build !windows

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestStatusOK exercises the happy path: dial+handshake against the
// fixture's in-process gateway succeeds and the human output reports
// connection: ok with a handshake duration. We go through runStatus
// (not RunE) so the test can inject env.cliCfg via opts.TLSConfig.
func TestStatusOK(t *testing.T) {
	env := setupPhase4(t, &config.DaemonConfig{})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newStatusCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runStatus(cmd, opts); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "connection:  ok") {
		t.Errorf("output missing 'connection:  ok':\n%s", got)
	}
	if !strings.Contains(got, "target:      lab01") {
		t.Errorf("output missing target line:\n%s", got)
	}
}
