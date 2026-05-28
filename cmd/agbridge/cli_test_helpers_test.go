package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/state"
)

// writeFixtureState writes a state.json pointing at env's gateway into a
// fresh temp dir and returns the dir. Tests that exercise loadOpts via
// the RunE wrapper should set g.stateDir = dir before invoking the cobra
// Command. The cert pin is a placeholder (sha256: + 64 zeros) — because
// Phase 2 fixtures use the testcert with a random fingerprint, callers
// must inject opts.TLSConfig via (*phase4Env).newCLIOpts() when going
// through runXxx() directly. loadOpts() does not honor the pin override,
// so tests that call cobra.RunE will fail TLS handshake; that's by design
// — those tests should use runXxx(cmd, args, env.newCLIOpts()) instead.
func writeFixtureState(t *testing.T, env *phase4Env) string {
	t.Helper()
	dir := t.TempDir()
	st := &state.BridgeState{
		Version:       state.CurrentVersion,
		AgentName:     "claude-laptop",
		APIKey:        "api-key-1",
		Gateway:       state.GatewayRef{URL: env.gatewayURL, TLSMode: "pin", CertPin: "sha256:" + strings.Repeat("0", 64)},
		E2EMode:       "disabled",
		DefaultTarget: "lab01",
		KnownDaemons: map[string]state.DaemonRef{
			"lab01": {DeviceID: "d", Pubkey: "p", PinSource: "tofu", FirstSeen: time.Now()},
		},
	}
	if err := state.SaveBridge(filepath.Join(dir, "state.json"), st); err != nil {
		t.Fatal(err)
	}
	return dir
}
