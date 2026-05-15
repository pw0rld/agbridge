package gateway

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
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func newTestConfig(t *testing.T) *config.GatewayConfig {
	t.Helper()
	auditP := filepath.Join(t.TempDir(), "audit.jsonl")
	return &config.GatewayConfig{
		Listen:    "127.0.0.1:0",
		AuditPath: auditP,
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
}

func sendHello(t *testing.T, ctx context.Context, c *wss.Conn, h handshake.Hello) {
	t.Helper()
	payload, err := h.Encode()
	if err != nil {
		t.Fatalf("hello encode: %v", err)
	}
	if err := c.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: payload}); err != nil {
		t.Fatalf("hello send: %v", err)
	}
}

func TestHandshakeBridgeOK(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := audit.Open(filepath.Join(t.TempDir(), "a.jsonl"))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer w.Close()
	cfg := newTestConfig(t)
	inst, err := Run(ctx, srvCfg, cfg, w)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	u := url.URL{Scheme: "wss", Host: inst.Addr.String(), Path: "/"}
	c, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	sendHello(t, ctx, c, handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"})
	deadline, _ := context.WithTimeout(ctx, 2*time.Second)
	ack, err := c.Recv(deadline)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ack.Type != proto.FrameTypeHelloAck {
		t.Errorf("got %v, want HelloAck", ack.Type)
	}
}

func TestHandshakeBridgeBadSecret(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := audit.Open(filepath.Join(t.TempDir(), "a.jsonl"))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer w.Close()
	cfg := newTestConfig(t)
	inst, err := Run(ctx, srvCfg, cfg, w)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	u := url.URL{Scheme: "wss", Host: inst.Addr.String(), Path: "/"}
	c, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	sendHello(t, ctx, c, handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "WRONG", TargetDaemon: "lab01"})
	deadline, _ := context.WithTimeout(ctx, 2*time.Second)
	f, err := c.Recv(deadline)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if f.Type != proto.FrameTypeError {
		t.Errorf("got %v, want Error", f.Type)
	}
}

func TestBridgePingRoutesToDaemon(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := audit.Open(filepath.Join(t.TempDir(), "a.jsonl"))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	defer w.Close()
	cfg := newTestConfig(t)
	inst, err := Run(ctx, srvCfg, cfg, w)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	u := url.URL{Scheme: "wss", Host: inst.Addr.String(), Path: "/"}

	// daemon side
	daemonConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	defer daemonConn.Close()
	sendHello(t, ctx, daemonConn, handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"})
	if ack, err := daemonConn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v %v", err, ack.Type)
	}

	// daemon goroutine: read Route frames and reply Pong on Ping
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f, err := daemonConn.Recv(ctx)
		if err != nil || f.Type != proto.FrameTypeRoute {
			t.Errorf("daemon recv: %v %v", err, f.Type)
			return
		}
		inner, err := proto.Decode(f.Payload)
		if err != nil {
			t.Errorf("daemon inner decode: %v", err)
			return
		}
		if inner.Type != proto.FrameTypePing {
			t.Errorf("daemon got %v inside route, want Ping", inner.Type)
			return
		}
		pong, _ := proto.Frame{Type: proto.FrameTypePong, ReqID: inner.ReqID}.Encode()
		_ = daemonConn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: pong})
	}()

	// bridge side
	bridgeConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("bridge dial: %v", err)
	}
	defer bridgeConn.Close()
	sendHello(t, ctx, bridgeConn, handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"})
	if ack, err := bridgeConn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("bridge hello ack: %v %v", err, ack.Type)
	}
	innerPing, _ := proto.Frame{Type: proto.FrameTypePing, ReqID: "r10"}.Encode()
	signed := auth.SignFrame([]byte("api-key-1"), innerPing)
	if err := bridgeConn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed}); err != nil {
		t.Fatalf("bridge send route: %v", err)
	}
	deadline, _ := context.WithTimeout(ctx, 2*time.Second)
	resp, err := bridgeConn.Recv(deadline)
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
	if inner.Type != proto.FrameTypePong || inner.ReqID != "r10" {
		t.Errorf("inner: %+v", inner)
	}
	wg.Wait()
}
