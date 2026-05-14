package main

import (
	"context"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func TestSmokePhase2EndToEnd(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	aud, err := audit.Open(auditPath)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer aud.Close()

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
	addr, err := gateway.Run(ctx, srvCfg, cfg, aud)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	u := (&url.URL{Scheme: "wss", Host: addr.String(), Path: "/"}).String()

	// daemon
	dconn, err := wss.Dial(ctx, u, transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	defer dconn.Close()
	dh, _ := handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"}.Encode()
	_ = dconn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: dh})
	if ack, _ := dconn.Recv(ctx); ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v", ack.Type)
	}
	var dwg sync.WaitGroup
	dwg.Add(1)
	go func() {
		defer dwg.Done()
		f, _ := dconn.Recv(ctx)
		if f.Type != proto.FrameTypeRoute {
			t.Errorf("daemon got %v, want Route", f.Type)
			return
		}
		inner, _ := proto.Decode(f.Payload)
		pong, _ := proto.Frame{Type: proto.FrameTypePong, ReqID: inner.ReqID}.Encode()
		_ = dconn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: pong})
	}()

	// bridge
	bconn, err := wss.Dial(ctx, u, transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("bridge dial: %v", err)
	}
	defer bconn.Close()
	bh, _ := handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"}.Encode()
	_ = bconn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: bh})
	if ack, _ := bconn.Recv(ctx); ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("bridge hello ack: %v", ack.Type)
	}
	innerPing, _ := proto.Frame{Type: proto.FrameTypePing, ReqID: "smoke-r1"}.Encode()
	signed := auth.SignFrame([]byte("api-key-1"), innerPing)
	_ = bconn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed})
	deadline, _ := context.WithTimeout(ctx, 3*time.Second)
	resp, err := bconn.Recv(deadline)
	if err != nil {
		t.Fatalf("bridge recv: %v", err)
	}
	if resp.Type != proto.FrameTypeRoute {
		t.Fatalf("bridge got %v, want Route", resp.Type)
	}
	inner, err := proto.Decode(resp.Payload)
	if err != nil {
		t.Fatalf("decode inner: %v", err)
	}
	if inner.Type != proto.FrameTypePong || inner.ReqID != "smoke-r1" {
		t.Errorf("inner: %+v", inner)
	}
	dwg.Wait()
}
