package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/mcp"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// phase4Env is the shared gateway+daemon+bridge harness. The MCP server in
// `srv` already has exec/read_file/write_file/port_forward registered. The
// gatewayURL and cliCfg are exposed so Phase 5 reconnect tests can drop the
// bridge conn and re-dial without rebuilding the whole stack.
type phase4Env struct {
	ctx        context.Context
	srv        *mcp.Server
	rt         *router
	gatewayURL string
	cliCfg     *tls.Config
}

// reconnectBridge closes the bridge's current wss conn, waits for runReader
// to notice, then re-dials + re-handshakes and attaches the new conn via
// router.replaceConn. Returns once a fresh runReader goroutine is running.
func (e *phase4Env) reconnectBridge(t *testing.T) {
	t.Helper()
	if c := e.rt.currentConn(); c != nil {
		_ = c.Close()
	}
	// Wait briefly for runReader to exit on the dead conn.
	time.Sleep(50 * time.Millisecond)
	newConn, err := wss.Dial(e.ctx, e.gatewayURL, transport.Credentials{}, e.cliCfg)
	if err != nil {
		t.Fatalf("redial: %v", err)
	}
	bh, _ := handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"}.Encode()
	if err := newConn.Send(e.ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: bh}); err != nil {
		t.Fatalf("rehandshake send: %v", err)
	}
	if ack, _ := newConn.Recv(e.ctx); ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("rehandshake ack: %v", ack.Type)
	}
	e.rt.replaceConn(newConn)
	go e.rt.runReader()
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

	bconn, err := wss.Dial(ctx, u, transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("bridge dial: %v", err)
	}
	t.Cleanup(func() { bconn.Close() })
	bh, _ := handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "api-key-1", TargetDaemon: "lab01"}.Encode()
	_ = bconn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: bh})
	if ack, _ := bconn.Recv(ctx); ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("bridge hello ack: %v", ack.Type)
	}
	rt := newRouter(ctx, bconn, []byte("api-key-1"))
	go rt.runReader()

	srv := mcp.NewServer()
	srv.RegisterTool(mcp.ToolSpec{Name: "exec", InputSchema: map[string]any{"type": "object"}}, rt.execHandler)
	srv.RegisterTool(mcp.ToolSpec{Name: "read_file", InputSchema: map[string]any{"type": "object"}}, rt.readFileHandler)
	srv.RegisterTool(mcp.ToolSpec{Name: "write_file", InputSchema: map[string]any{"type": "object"}}, rt.writeFileHandler)
	srv.RegisterTool(mcp.ToolSpec{Name: "port_forward", InputSchema: map[string]any{"type": "object"}}, rt.portForwardHandler)

	return &phase4Env{ctx: ctx, srv: srv, rt: rt, gatewayURL: u, cliCfg: cliCfg}
}

// callTool drives one tools/call round-trip via stdin/stdout JSON-RPC and
// returns the `result` object. Fails the test on transport / unmarshal errors.
func callTool(t *testing.T, env *phase4Env, name string, args map[string]any) map[string]any {
	t.Helper()
	argsJSON, _ := json.Marshal(args)
	in := strings.NewReader(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":%s}}`+"\n",
		name, string(argsJSON),
	))
	var out bytes.Buffer
	doneCh := make(chan struct{})
	go func() {
		_ = env.srv.Serve(env.ctx, in, &out)
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(15 * time.Second):
		t.Fatalf("server did not return in time; output so far: %s", out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 response lines, got %d: %v", len(lines), lines)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, lines[1])
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", resp)
	}
	return res
}

func TestSmokePhase3ExecEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		AllowedExecCwds: []string{tmpDir + "/*", tmpDir},
		EnvAllowlist:    []string{"PATH"},
	})
	res := callTool(t, env, "exec", map[string]any{
		"cmd":        "/bin/echo",
		"args":       []string{"phase3"},
		"cwd":        tmpDir,
		"timeout_ms": 5000,
	})
	meta := res["_meta"].(map[string]any)
	if int(meta["exitcode"].(float64)) != 0 {
		t.Errorf("exitcode: %v", meta["exitcode"])
	}
	if s, ok := meta["stdout_b64"].(string); !ok || s == "" {
		t.Errorf("stdout_b64 missing: %+v", meta)
	}
}

func TestSmokePhase4ReadFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "hello.txt")
	content := []byte("hello phase4 read\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	want := sha256.Sum256(content)
	wantHex := hex.EncodeToString(want[:])

	env := setupPhase4(t, &config.DaemonConfig{
		AllowedReadPaths: []string{tmpDir + "/*", tmpDir},
	})
	res := callTool(t, env, "read_file", map[string]any{"path": path})
	meta := res["_meta"].(map[string]any)
	if int64(meta["size"].(float64)) != int64(len(content)) {
		t.Errorf("size: got %v want %d", meta["size"], len(content))
	}
	if got := meta["sha256"].(string); got != wantHex {
		t.Errorf("sha256: got %s want %s", got, wantHex)
	}
	gotB64 := meta["content_b64"].(string)
	gotBytes, err := base64.StdEncoding.DecodeString(gotB64)
	if err != nil {
		t.Fatalf("decode content_b64: %v", err)
	}
	if !bytes.Equal(gotBytes, content) {
		t.Errorf("content mismatch: got %q want %q", gotBytes, content)
	}
}

func TestSmokePhase4WriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "out.txt")
	content := []byte("phase4 write test\n")
	wantSha := sha256.Sum256(content)
	wantHex := hex.EncodeToString(wantSha[:])

	env := setupPhase4(t, &config.DaemonConfig{
		AllowedWritePaths: []string{tmpDir + "/*", tmpDir},
	})
	res := callTool(t, env, "write_file", map[string]any{
		"path":        path,
		"content_b64": base64.StdEncoding.EncodeToString(content),
		"mode":        0o600,
	})
	meta := res["_meta"].(map[string]any)
	if int64(meta["bytes_written"].(float64)) != int64(len(content)) {
		t.Errorf("bytes_written: got %v want %d", meta["bytes_written"], len(content))
	}
	if got := meta["sha256"].(string); got != wantHex {
		t.Errorf("sha256: got %s want %s", got, wantHex)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(onDisk, content) {
		t.Errorf("on-disk content mismatch: got %q want %q", onDisk, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode: got %v want 0600", info.Mode().Perm())
	}
}

func TestSmokePhase4PortForward(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()

	env := setupPhase4(t, &config.DaemonConfig{})
	res := callTool(t, env, "port_forward", map[string]any{
		"remote_host": "127.0.0.1",
		"remote_port": echoPort,
	})
	meta := res["_meta"].(map[string]any)
	localPort := int(meta["local_port"].(float64))
	if localPort == 0 {
		t.Fatalf("local_port missing: %+v", meta)
	}

	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial local: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello phase4\n")
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := readFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Errorf("echo mismatch: got %q want %q", got, msg)
	}
}

func TestSmokePhase5ReconnectAfterDrop(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		AllowedExecCwds: []string{tmpDir + "/*", tmpDir},
		EnvAllowlist:    []string{"PATH"},
	})

	// 1. Baseline call works.
	res := callTool(t, env, "exec", map[string]any{
		"cmd":  "/bin/echo",
		"args": []string{"before"},
		"cwd":  tmpDir,
	})
	if int(res["_meta"].(map[string]any)["exitcode"].(float64)) != 0 {
		t.Fatalf("baseline exitcode != 0: %+v", res["_meta"])
	}

	// 2. Drop the bridge↔gateway socket and re-attach a fresh conn.
	env.reconnectBridge(t)

	// 3. Subsequent call goes through the new conn.
	res = callTool(t, env, "exec", map[string]any{
		"cmd":  "/bin/echo",
		"args": []string{"after"},
		"cwd":  tmpDir,
	})
	if int(res["_meta"].(map[string]any)["exitcode"].(float64)) != 0 {
		t.Fatalf("post-reconnect exitcode != 0: %+v", res["_meta"])
	}
	stdoutB64 := res["_meta"].(map[string]any)["stdout_b64"].(string)
	out, _ := base64.StdEncoding.DecodeString(stdoutB64)
	if !bytes.Contains(out, []byte("after")) {
		t.Errorf("expected 'after' in stdout, got %q", out)
	}
}

func TestSmokePhase5SIGHUPRevoke(t *testing.T) {
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
		Agents: []config.AgentEntry{
			{Name: "alpha", APIKeyHash: "sha256:" + auth.SHA256Hex([]byte("k-alpha")), AllowedDaemons: []string{"lab01"}},
			{Name: "bravo", APIKeyHash: "sha256:" + auth.SHA256Hex([]byte("k-bravo")), AllowedDaemons: []string{"lab01"}},
		},
		Daemons: []config.DaemonEntry{
			{Name: "lab01", TokenHash: "sha256:" + auth.SHA256Hex([]byte("daemon-tok-1"))},
		},
	}
	inst, err := gateway.Run(ctx, srvCfg, cfg, aud)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	u := (&url.URL{Scheme: "wss", Host: inst.Addr.String(), Path: "/"}).String()

	// Daemon online so both bridges' handshake passes (TargetDaemon=lab01).
	dconn, err := wss.Dial(ctx, u, transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	defer dconn.Close()
	dh, _ := handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"}.Encode()
	_ = dconn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: dh})
	if ack, _ := dconn.Recv(ctx); ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon ack: %v", ack.Type)
	}
	go runFakeDaemon(ctx, dconn, &config.DaemonConfig{})

	dialBridge := func(name, secret string) *wss.Conn {
		c, err := wss.Dial(ctx, u, transport.Credentials{}, cliCfg)
		if err != nil {
			t.Fatalf("dial %s: %v", name, err)
		}
		bh, _ := handshake.Hello{Role: "bridge", Name: name, Secret: secret, TargetDaemon: "lab01"}.Encode()
		_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: bh})
		if ack, _ := c.Recv(ctx); ack.Type != proto.FrameTypeHelloAck {
			t.Fatalf("%s ack: %v", name, ack.Type)
		}
		return c
	}
	alphaConn := dialBridge("alpha", "k-alpha")
	defer alphaConn.Close()
	bravoConn := dialBridge("bravo", "k-bravo")
	defer bravoConn.Close()

	// Reload config: remove alpha.
	newCfg := &config.GatewayConfig{
		Listen:    cfg.Listen,
		AuditPath: cfg.AuditPath,
		Agents: []config.AgentEntry{
			{Name: "bravo", APIKeyHash: "sha256:" + auth.SHA256Hex([]byte("k-bravo")), AllowedDaemons: []string{"lab01"}},
		},
		Daemons: cfg.Daemons,
	}
	inst.Creds.Replace(newCfg)
	revoked := inst.Sessions.Revoke(inst.Creds)
	if len(revoked) != 1 || revoked[0] != "bridge/alpha" {
		t.Errorf("revoked = %v, want [bridge/alpha]", revoked)
	}

	// Alpha's conn should error on Recv within ~200ms.
	alphaCtx, alphaCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer alphaCancel()
	if _, err := alphaConn.Recv(alphaCtx); err == nil {
		t.Errorf("alpha conn still alive after revoke")
	}

	// Bravo's conn should still work — send a Route frame and expect no
	// immediate error. (Receiving a valid response requires the full
	// daemon/router stack; we only assert the conn hasn't been closed.)
	bravoCtx, bravoCancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer bravoCancel()
	if err := bravoConn.Send(bravoCtx, proto.Frame{Type: proto.FrameTypePing, ReqID: "k"}); err != nil {
		t.Errorf("bravo conn died too: %v", err)
	}
}

func TestSmokePhase5InflightCallReturnsNetworkLost(t *testing.T) {
	tmpDir := t.TempDir()
	env := setupPhase4(t, &config.DaemonConfig{
		AllowedExecCwds: []string{tmpDir + "/*", tmpDir},
		EnvAllowlist:    []string{"PATH"},
	})

	// Register a reqID with a real channel, then drop the conn — the
	// handler-style consumer should observe the closed channel via
	// replaceConn(nil) and would translate to network_lost in the real
	// handler path.
	reqID := newReqID()
	ch := env.rt.registerCall(reqID)
	defer env.rt.unregisterCall(reqID)

	// Force a "drop" by signaling pending.
	env.rt.replaceConn(nil)

	select {
	case _, ok := <-ch:
		if ok {
			t.Errorf("channel yielded a value, expected closed")
		}
	case <-time.After(time.Second):
		t.Fatalf("channel not closed after replaceConn(nil)")
	}
}

func readFull(c net.Conn, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := c.Read(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
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
