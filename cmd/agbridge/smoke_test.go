package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/mcp"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/tools"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func TestSmokePhase3ExecEndToEnd(t *testing.T) {
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
	tmpDir := t.TempDir()
	dcfg := &config.DaemonConfig{
		AllowedExecCwds: []string{tmpDir + "/*", tmpDir},
		EnvAllowlist:    []string{"PATH"},
	}
	go runFakeDaemon(ctx, dconn, dcfg, t)

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
	rt := newRouter(ctx, bconn, []byte("api-key-1"))
	go rt.runReader()

	srv := mcp.NewServer()
	srv.RegisterTool(mcp.ToolSpec{
		Name:        "exec",
		Description: "Run a command",
		InputSchema: map[string]any{"type": "object"},
	}, rt.execHandler)

	args := map[string]any{"cmd": "/bin/echo", "args": []string{"phase3"}, "cwd": tmpDir, "timeout_ms": 5000}
	argsJSON, _ := json.Marshal(args)
	in := strings.NewReader(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"exec","arguments":%s}}`+"\n",
		string(argsJSON),
	))
	var out bytes.Buffer

	doneCh := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx, in, &out)
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("server did not return in time; output so far: %s", out.String())
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 response lines, got %d: %v", len(lines), lines)
	}
	var resp2 map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp2); err != nil {
		t.Fatalf("unmarshal: %v: %s", err, lines[1])
	}
	res, ok := resp2["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", resp2)
	}
	meta := res["_meta"].(map[string]any)
	if int(meta["exitcode"].(float64)) != 0 {
		t.Errorf("exitcode: %v", meta["exitcode"])
	}
	if s, ok := meta["stdout_b64"].(string); !ok || s == "" {
		t.Errorf("stdout_b64 missing: %+v", meta)
	}
}

func runFakeDaemon(ctx context.Context, conn *wss.Conn, cfg *config.DaemonConfig, t *testing.T) {
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
		if inner.Type != proto.FrameTypeExecRequest {
			continue
		}
		req, err := execproto.DecodeExecRequest(inner.Payload)
		if err != nil {
			t.Errorf("decode exec request: %v", err)
			continue
		}
		handleDaemonSide(ctx, conn, inner, cfg, req)
	}
}

func handleDaemonSide(ctx context.Context, conn *wss.Conn, inner proto.Frame, cfg *config.DaemonConfig, req execproto.ExecRequest) {
	onChunk := func(c execproto.ExecChunk) {
		payload, _ := c.Encode()
		chunkFrame, _ := proto.Frame{Type: proto.FrameTypeExecChunk, ReqID: inner.ReqID, Payload: payload}.Encode()
		_ = conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: chunkFrame})
	}
	complete, err := tools.Exec(ctx, req, cfg.AllowedExecCwds, cfg.EnvAllowlist, onChunk)
	if err != nil {
		errFrame, _ := proto.Frame{Type: proto.FrameTypeError, ReqID: inner.ReqID, Payload: []byte("exec_failed")}.Encode()
		_ = conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: errFrame})
		return
	}
	completePayload, _ := complete.Encode()
	completeFrame, _ := proto.Frame{Type: proto.FrameTypeExecComplete, ReqID: inner.ReqID, Payload: completePayload}.Encode()
	_ = conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: completeFrame})
}
