package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/agentsock"
	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/config"
)

// fakeCaller is the agentCaller test double.
type fakeCaller struct {
	mu     sync.Mutex
	calls  int
	closed bool
	resp   any
	err    error
}

func (f *fakeCaller) Dispatch(context.Context, string, json.RawMessage) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.resp, f.err
}

func (f *fakeCaller) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeCaller) stats() (calls int, closed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.closed
}

func mustOpts(t *testing.T, o cliclient.Options) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAgentPoolReusesOneConnectionPerKey(t *testing.T) {
	var dials int32
	var made []*fakeCaller
	var mkMu sync.Mutex
	p := &agentPool{
		entries:     map[string]*agentPoolEntry{},
		idleTimeout: time.Minute,
		dial: func(context.Context, cliclient.Options) (agentCaller, error) {
			atomic.AddInt32(&dials, 1)
			f := &fakeCaller{resp: map[string]any{"ok": true}}
			mkMu.Lock()
			made = append(made, f)
			mkMu.Unlock()
			return f, nil
		},
	}
	opts := cliclient.Options{GatewayURL: "wss://gw:8443", AgentName: "a", Target: "t1"}
	req := agentsock.Request{Handler: "stat", Args: json.RawMessage(`{"path":"/tmp"}`), Opts: mustOpts(t, opts)}

	for i := 0; i < 3; i++ {
		res, err := p.handle(context.Background(), req)
		if err != nil {
			t.Fatalf("handle %d: %v", i, err)
		}
		if m, ok := res.(map[string]any); !ok || m["ok"] != true {
			t.Fatalf("handle %d result = %+v", i, res)
		}
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
	if calls, _ := made[0].stats(); calls != 3 {
		t.Fatalf("calls on pooled caller = %d, want 3", calls)
	}

	// A different target is a different pool key.
	req2 := req
	req2.Opts = mustOpts(t, cliclient.Options{GatewayURL: "wss://gw:8443", AgentName: "a", Target: "t2"})
	if _, err := p.handle(context.Background(), req2); err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("dials after second target = %d, want 2", dials)
	}
	st := p.status()
	if st["connections"] != 2 {
		t.Fatalf("status connections = %v, want 2", st["connections"])
	}
}

func TestAgentPoolEvictsOnNetworkLost(t *testing.T) {
	networkLost := map[string]any{
		"isError": true,
		"content": []any{},
		"_meta":   map[string]any{"error_code": "network_lost", "category": "transport", "retryable": true, "message": "connection reset"},
	}
	var dials int32
	var made []*fakeCaller
	p := &agentPool{
		entries:     map[string]*agentPoolEntry{},
		idleTimeout: time.Minute,
		dial: func(context.Context, cliclient.Options) (agentCaller, error) {
			f := &fakeCaller{resp: networkLost}
			if atomic.AddInt32(&dials, 1) == 2 {
				f.resp = map[string]any{"ok": true} // second life heals
			}
			made = append(made, f)
			return f, nil
		},
	}
	opts := cliclient.Options{GatewayURL: "wss://gw:8443", Target: "t1"}
	req := agentsock.Request{Handler: "exec", Args: json.RawMessage(`{}`), Opts: mustOpts(t, opts)}

	res, err := p.handle(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := res.(map[string]any); m["isError"] != true {
		t.Fatalf("first handle should surface the network_lost envelope, got %+v", res)
	}
	if _, closed := made[0].stats(); !closed {
		t.Fatal("dead caller must be closed on eviction")
	}
	if st := p.status(); st["connections"] != 0 {
		t.Fatalf("evicted entry must leave the pool, status=%v", st)
	}

	res, err = p.handle(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := res.(map[string]any); m["ok"] != true {
		t.Fatalf("second handle should re-dial and succeed, got %+v", res)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

func TestAgentPoolDialFailureRetries(t *testing.T) {
	var dials int32
	p := &agentPool{
		entries:     map[string]*agentPoolEntry{},
		idleTimeout: time.Minute,
		dial: func(context.Context, cliclient.Options) (agentCaller, error) {
			if atomic.AddInt32(&dials, 1) == 1 {
				return nil, errors.New("dial: no route")
			}
			return &fakeCaller{resp: map[string]any{"ok": true}}, nil
		},
	}
	opts := cliclient.Options{GatewayURL: "wss://gw:8443", Target: "t1"}
	req := agentsock.Request{Handler: "stat", Args: json.RawMessage(`{"path":"/tmp"}`), Opts: mustOpts(t, opts)}

	if _, err := p.handle(context.Background(), req); err == nil {
		t.Fatal("first handle should fail the dial")
	}
	if st := p.status(); st["connections"] != 0 {
		t.Fatalf("failed dial must not leave a pool entry, status=%v", st)
	}
	if _, err := p.handle(context.Background(), req); err != nil {
		t.Fatalf("second handle: %v", err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

func TestAgentPoolGCClosesIdle(t *testing.T) {
	f := &fakeCaller{resp: map[string]any{"ok": true}}
	p := &agentPool{
		entries:     map[string]*agentPoolEntry{},
		idleTimeout: time.Minute,
		dial:        func(context.Context, cliclient.Options) (agentCaller, error) { return f, nil },
	}
	opts := cliclient.Options{GatewayURL: "wss://gw:8443", Target: "t1"}
	e, err := p.acquire(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	e.lastUsed.Store(time.Now().Add(-2 * time.Minute).UnixNano())
	e.release()

	p.gcOnce()
	if _, closed := f.stats(); !closed {
		t.Fatal("idle caller should be closed by gc")
	}
	if st := p.status(); st["connections"] != 0 {
		t.Fatalf("gc should empty the pool, status=%v", st)
	}
}

func TestAgentPoolConnectAndStatusHandlers(t *testing.T) {
	var dials int32
	p := &agentPool{
		entries:     map[string]*agentPoolEntry{},
		idleTimeout: time.Minute,
		dial: func(context.Context, cliclient.Options) (agentCaller, error) {
			atomic.AddInt32(&dials, 1)
			return &fakeCaller{resp: map[string]any{"ok": true}}, nil
		},
	}
	opts := cliclient.Options{GatewayURL: "wss://gw:8443", Target: "t1"}

	// __status__ needs no opts and dials nothing.
	st, err := p.handle(context.Background(), agentsock.Request{Handler: "__status__"})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := st.(map[string]any); m["connections"] != 0 {
		t.Fatalf("empty pool status = %+v", st)
	}
	if dials != 0 {
		t.Fatal("__status__ must not dial")
	}

	// __connect__ dials but dispatches nothing.
	res, err := p.handle(context.Background(), agentsock.Request{Handler: "__connect__", Opts: mustOpts(t, opts)})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := res.(map[string]any); m["connected"] != true {
		t.Fatalf("__connect__ result = %+v", res)
	}
	if dials != 1 {
		t.Fatalf("__connect__ dials = %d, want 1", dials)
	}

	// Missing opts on a tool handler is a plumbing error.
	if _, err := p.handle(context.Background(), agentsock.Request{Handler: "stat"}); err == nil {
		t.Fatal("missing opts must error")
	}
}

// TestAgentSocketPathEnv covers the routing switch env contract.
func TestAgentSocketPathEnv(t *testing.T) {
	t.Setenv("AGBRIDGE_NO_AGENT", "1")
	if got := agentSocketPath(); got != "" {
		t.Fatalf("AGBRIDGE_NO_AGENT=1 → %q, want empty", got)
	}
	t.Setenv("AGBRIDGE_NO_AGENT", "")
	t.Setenv("AGBRIDGE_AGENT_SOCKET", "off")
	if got := agentSocketPath(); got != "" {
		t.Fatalf("AGBRIDGE_AGENT_SOCKET=off → %q, want empty", got)
	}
	t.Setenv("AGBRIDGE_AGENT_SOCKET", "/tmp/custom-agent.sock")
	if got := agentSocketPath(); got != "/tmp/custom-agent.sock" {
		t.Fatalf("explicit env → %q", got)
	}
}

// TestAgentBrokerEndToEnd drives invokeRouted through a live broker
// backed by the in-process gateway+daemon fixture: proves the agent
// path serves real traffic, reuses one connection across calls, and
// survives the JSON hop (result maps identical to the classic path).
func TestAgentBrokerEndToEnd(t *testing.T) {
	env := setupPhase4(t, &config.DaemonConfig{
		ExecEnabled:  true,
		EnvAllowlist: []string{"PATH"},
	})
	opts := env.newCLIOpts()

	var dials int32
	pool := &agentPool{
		entries:     map[string]*agentPoolEntry{},
		idleTimeout: 10 * time.Minute,
		dial: func(ctx context.Context, o cliclient.Options) (agentCaller, error) {
			atomic.AddInt32(&dials, 1)
			o.TLSConfig = env.cliCfg // in-process harness cert, injected like newCLIOpts
			return cliclient.Connect(ctx, o)
		},
	}
	// Short temp dir: macOS sun_path caps UDS paths at ~104 bytes.
	agentDir, err := os.MkdirTemp("", "abr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(agentDir) })
	sock := agentDir + "/agent.sock"
	ctx, cancel := context.WithTimeout(env.ctx, 20*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- agentsock.Serve(ctx, sock, pool.handle, agentsock.ServerOptions{}) }()
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		select {
		case err := <-serveErr:
			t.Fatalf("agent serve exited early: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}

	opts.AgentSocket = sock
	for i := 0; i < 2; i++ {
		res, err := invokeRouted(ctx, opts, "exec", map[string]any{
			"cmd":        "/bin/echo",
			"args":       []string{"via-agent"},
			"timeout_ms": 5000,
		})
		if err != nil {
			t.Fatalf("invokeRouted %d: %v", i, err)
		}
		rmap, ok := res.(map[string]any)
		if !ok {
			t.Fatalf("result shape %T", res)
		}
		if isErr, _ := rmap["isError"].(bool); isErr {
			t.Fatalf("exec via agent errored: %+v", rmap)
		}
		meta, _ := rmap["_meta"].(map[string]any)
		if s, _ := meta["stdout"].(string); s != "via-agent\n" {
			t.Fatalf("call %d stdout = %q, want %q", i, meta["stdout"], "via-agent\n")
		}
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want exactly 1 (connection reused)", dials)
	}

	st, handled, err := agentStatus(ctx, sock)
	if !handled || err != nil {
		t.Fatalf("agentStatus: handled=%v err=%v", handled, err)
	}
	if st["connections"] != float64(1) {
		t.Fatalf("status = %+v, want 1 connection", st)
	}
}
