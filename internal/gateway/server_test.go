package gateway

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	deadline, dcancel := context.WithTimeout(ctx, 2*time.Second)
	defer dcancel()
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
	deadline, dcancel := context.WithTimeout(ctx, 2*time.Second)
	defer dcancel()
	f, err := c.Recv(deadline)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if f.Type != proto.FrameTypeError {
		t.Errorf("got %v, want Error", f.Type)
	}
}

// TestBridgeDuplicateIdentitySendsSessionReplaced verifies F7: when a
// second bridge with the same (name, api_key) connects, the gateway sends
// a session_replaced error frame to the older bridge before closing its
// conn. Without this signal the older bridge's supervisor would
// auto-reconnect and enter a kick-storm with the newer instance.
// TestBridgeDaemonProtocolMismatchRefused registers a generation-2 daemon,
// then connects a generation-1 bridge (Hello wire bytes without the
// trailing version byte, exactly what v0.0.3 clients send) and asserts the
// gateway pushes protocol_mismatch instead of relaying.
func TestBridgeDaemonProtocolMismatchRefused(t *testing.T) {
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

	dconn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	defer dconn.Close()
	sendHello(t, ctx, dconn, handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1", ProtocolVersion: 2})
	if ack, err := dconn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v %v", ack.Type, err)
	}

	bconn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer bconn.Close()
	// Simulate a pre-v0.0.5 bridge: valid generation-1 Hello wire, i.e.
	// everything except the trailing protocol-version byte.
	payload, err := (handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := bconn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: payload[:len(payload)-1]}); err != nil {
		t.Fatal(err)
	}
	if ack, err := bconn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("bridge hello ack: %v %v", ack.Type, err)
	}
	f, err := bconn.Recv(ctx)
	if err != nil {
		t.Fatalf("recv mismatch frame: %v", err)
	}
	if f.Type != proto.FrameTypeError || string(f.Payload) != "protocol_mismatch" {
		t.Fatalf("got type=%v payload=%q, want protocol_mismatch error", f.Type, f.Payload)
	}
}

func TestBridgeDuplicateIdentitySendsSessionReplaced(t *testing.T) {
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

	// Bring up a daemon so the bridges' handleBridge stays in its read
	// loop (without one, both would early-exit with daemon_offline before
	// the replace path is exercised).
	daemonConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	defer daemonConn.Close()
	sendHello(t, ctx, daemonConn, handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"})
	if ack, err := daemonConn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v %v", err, ack.Type)
	}

	// Bridge A — first to arrive.
	bridgeA, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("bridgeA dial: %v", err)
	}
	defer bridgeA.Close()
	sendHello(t, ctx, bridgeA, handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"})
	if ack, err := bridgeA.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("bridgeA hello ack: %v %v", err, ack.Type)
	}

	// Bridge B — same identity, displaces A.
	bridgeB, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("bridgeB dial: %v", err)
	}
	defer bridgeB.Close()
	sendHello(t, ctx, bridgeB, handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"})
	if ack, err := bridgeB.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("bridgeB hello ack: %v %v", err, ack.Type)
	}

	// A must receive a session_replaced error frame, then its conn must
	// close. Allow a generous deadline so the test isn't flaky on slow CI.
	deadline, dcancel := context.WithTimeout(ctx, 3*time.Second)
	defer dcancel()
	f, err := bridgeA.Recv(deadline)
	if err != nil {
		t.Fatalf("bridgeA recv: %v", err)
	}
	if f.Type != proto.FrameTypeError {
		t.Fatalf("bridgeA got frame type %v, want Error", f.Type)
	}
	if string(f.Payload) != "session_replaced" {
		t.Errorf("bridgeA error payload = %q, want session_replaced", string(f.Payload))
	}

	// Subsequent Recv should fail (conn closed by gateway).
	_, err = bridgeA.Recv(deadline)
	if err == nil {
		t.Errorf("bridgeA conn should be closed after session_replaced; Recv returned no error")
	}
}

// TestAuditEmitsConnCloseOnDaemonDisconnect verifies the audit log is
// a complete connect/disconnect ledger: every auth_ok must be paired
// with a conn_close on the same name. Without this, downstream
// consumers (operators, dashboards) can't tell when a device went
// offline without polling.
func TestAuditEmitsConnCloseOnDaemonDisconnect(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auditPath := filepath.Join(t.TempDir(), "a.jsonl")
	w, err := audit.Open(auditPath)
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
	daemonConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	sendHello(t, ctx, daemonConn, handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"})
	if ack, err := daemonConn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v %v", err, ack.Type)
	}
	// Drop the daemon conn — the server-side handleDaemon read loop
	// returns and the deferred conn_close should fire.
	daemonConn.Close()
	// Defer runs asynchronously on the server side; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if auditContains(t, auditPath, "conn_close", "lab01") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !auditContains(t, auditPath, "auth_ok", "lab01") {
		t.Errorf("audit missing auth_ok for lab01")
	}
	if !auditContains(t, auditPath, "conn_close", "lab01") {
		t.Errorf("audit missing conn_close for lab01 after disconnect")
	}
}

// auditContains scans the audit JSONL file for a line whose event
// field matches `ev` and whose name field matches `name`. Returns
// false on any read/parse error — the test fails through the
// assert site, not here.
func auditContains(t *testing.T, path, ev, name string) bool {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["event"] == ev && rec["name"] == name {
			return true
		}
	}
	return false
}

// auditEventBytes scans the audit JSONL file for the first record whose
// event field matches `ev` and returns its "bytes" field. Returns
// (0, false) when no such record carries a numeric bytes field — which
// is exactly the "No byte metadata yet" condition the dashboard renders.
func auditEventBytes(t *testing.T, path, ev string) (int64, bool) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["event"] != ev {
			continue
		}
		if n, ok := rec["bytes"].(float64); ok {
			return int64(n), true
		}
	}
	return 0, false
}

// TestAuditRouteRecordsBytes verifies the gateway records the relayed
// frame size on route audit events. The cloud dashboard derives its
// Traffic figure from this field; without it usage shows a permanent
// "No byte metadata yet / 0 B". It is the gateway's only honest traffic
// signal — frames are E2E encrypted, so size is all it can see.
func TestAuditRouteRecordsBytes(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auditPath := filepath.Join(t.TempDir(), "a.jsonl")
	w, err := audit.Open(auditPath)
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

	daemonConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	defer daemonConn.Close()
	sendHello(t, ctx, daemonConn, handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"})
	if ack, err := daemonConn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v %v", err, ack.Type)
	}
	// Drain whatever the daemon receives so handleBridge completes its send.
	go func() { _, _ = daemonConn.Recv(ctx) }()

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

	// The route audit record is written before the frame is forwarded; poll
	// until it lands, then assert it carries a positive byte count.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, ok := auditEventBytes(t, auditPath, "route"); ok {
			if n <= 0 {
				t.Fatalf("route audit bytes = %d, want > 0", n)
			}
			if int64(len(signed)) != n {
				t.Errorf("route audit bytes = %d, want frame size %d", n, len(signed))
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("route audit record never carried a bytes field")
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
	deadline, dcancel := context.WithTimeout(ctx, 2*time.Second)
	defer dcancel()
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
