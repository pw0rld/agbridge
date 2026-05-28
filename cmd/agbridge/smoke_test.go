package main

import (
	"context"
	"crypto/tls"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// phase4Env is the shared gateway+fake-daemon harness for in-process CLI
// integration tests. v0.0.2 deletes the MCP bridge — the bridge side of
// the test harness is no longer needed because cli_*_test.go drives
// cliclient.Invoke directly. setupPhase4 only wires up the gateway and a
// fake daemon; per-test bridge dials happen inside cliclient.
//
// (The name "phase4Env" is historical — refactor in Phase 5 if useful.)
type phase4Env struct {
	ctx        context.Context
	gatewayURL string
	cliCfg     *tls.Config
}

// newCLIOpts builds a cliclient.Options pointing at this fixture's
// gateway. E2E is disabled (matches setupPhase4); TLS uses the testcert
// via direct TLSConfig injection (loadOpts() ignores TLSConfig because
// state.json carries a cert pin — fixture tests skip loadOpts and call
// runXxx() with opts inlined).
func (e *phase4Env) newCLIOpts() cliclient.Options {
	return cliclient.Options{
		GatewayURL: e.gatewayURL,
		AgentName:  "claude-laptop",
		APIKey:     "api-key-1",
		Target:     "lab01",
		E2EMode:    "disabled",
		TLSConfig:  e.cliCfg,
	}
}

func setupPhase4(t *testing.T, dcfg *config.DaemonConfig) *phase4Env {
	t.Helper()
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	aud, err := audit.Open(auditPath)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { aud.Close() })

	cfg := &config.GatewayConfig{
		Listen:    "127.0.0.1:0",
		AuditPath: auditPath,
		Agents: []config.AgentEntry{{
			Name:           "claude-laptop",
			APIKeyHash:     "sha256:" + auth.SHA256Hex([]byte("api-key-1")),
			AllowedDaemons: []string{"lab01"},
		}},
		Daemons: []config.DaemonEntry{{
			Name:      "lab01",
			TokenHash: "sha256:" + auth.SHA256Hex([]byte("daemon-tok-1")),
		}},
	}
	inst, err := gateway.Run(ctx, srvCfg, cfg, aud)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	u := (&url.URL{Scheme: "wss", Host: inst.Addr.String(), Path: "/"}).String()

	dconn, err := wss.Dial(ctx, u, transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	t.Cleanup(func() { dconn.Close() })
	dh, _ := handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"}.Encode()
	_ = dconn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: dh})
	if ack, _ := dconn.Recv(ctx); ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v", ack.Type)
	}
	go runFakeDaemon(ctx, dconn, dcfg)

	return &phase4Env{ctx: ctx, gatewayURL: u, cliCfg: cliCfg}
}

// runFakeDaemon drives a daemonState dispatch loop on a pre-handshaked wss
// connection. Reuses the production daemonState so tests exercise the same
// frame routing as the real daemon binary.
func runFakeDaemon(ctx context.Context, conn *wss.Conn, cfg *config.DaemonConfig) {
	state := &daemonState{
		cfg:     cfg,
		conn:    conn,
		writes:  make(map[string]*writeSlot),
		streams: make(map[string]*streamSlot),
	}
	for {
		f, err := conn.Recv(ctx)
		if err != nil {
			return
		}
		if f.Type != proto.FrameTypeRoute {
			continue
		}
		inner, err := proto.Decode(f.Payload)
		if err != nil {
			continue
		}
		state.dispatch(ctx, inner)
	}
}
