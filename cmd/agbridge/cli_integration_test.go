package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/config"
)

// TestCLIInvokeExecAgainstFixture is the bellwether for Phase 2: it
// proves cliclient.Invoke -> dial -> Hello -> Router.ExecHandler works
// end-to-end against the in-process gateway+daemon fixture, without
// MCP in the loop. Every Phase 2 CLI subcommand test reuses this
// pattern via (*phase4Env).newCLIOpts().
func TestCLIInvokeExecAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		AllowedExecCwds: []string{tmpDir, tmpDir + "/*"},
		EnvAllowlist:    []string{"PATH"},
	})
	opts := env.newCLIOpts()

	ctx, cancel := context.WithTimeout(env.ctx, 5*time.Second)
	defer cancel()
	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		raw, _ := json.Marshal(map[string]any{
			"cmd":        "/bin/echo",
			"args":       []string{"ok"},
			"cwd":        tmpDir,
			"timeout_ms": 5000,
		})
		return c.Router().ExecHandler(ctx, raw)
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
}
