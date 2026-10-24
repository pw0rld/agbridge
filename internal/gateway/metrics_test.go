package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// waitFor polls cond until true or the deadline passes. Counters that
// are recorded asynchronously (queue sender goroutine) need this.
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

func TestMetricsRenderAndNilSafety(t *testing.T) {
	m := NewMetrics()
	m.daemonConnected()
	m.bridgeConnected()
	m.noteDaemonInbound(10)
	m.noteToDaemon(12)
	m.noteQueueIn(7)
	m.noteQueueOut(7)
	m.noteToBridge(7)
	m.authFailed()
	m.authzFailed()
	m.macFailed()
	m.protocolMismatch()
	m.sessionReplaced()
	m.queueOverflow()
	m.daemonDisconnected()
	m.bridgeDisconnected()

	var buf bytes.Buffer
	m.Render(&buf)
	out := buf.String()
	for _, want := range []string{
		"agbridge_daemon_connections_active 0",
		"agbridge_daemon_connections_total 1",
		"agbridge_bridge_connections_active 0",
		"agbridge_frames_from_daemon_total 1",
		"agbridge_bytes_from_daemon_total 10",
		"agbridge_bytes_to_daemons_total 12",
		"agbridge_frames_to_bridges_total 1",
		"agbridge_bridge_queue_frames 0",
		"agbridge_bridge_queue_bytes 0",
		"agbridge_auth_failures_total 1",
		"agbridge_authz_failures_total 1",
		"agbridge_mac_failures_total 1",
		"agbridge_protocol_mismatches_total 1",
		"agbridge_session_replaced_total 1",
		"agbridge_bridge_queue_overflows_total 1",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("Render output missing %q", want)
		}
	}
	if !strings.Contains(out, "# TYPE agbridge_uptime_seconds gauge") {
		t.Error("Render output missing uptime metric")
	}

	// nil *Metrics must be usable everywhere: tests and embedded
	// gateways pass nil to disable instrumentation.
	var nilM *Metrics
	nilM.daemonConnected()
	nilM.noteToBridge(1)
	nilM.Render(io.Discard)

	// frameWireSize = 8-byte header + ReqID + Payload.
	if got := frameWireSize(proto.Frame{ReqID: "abc", Payload: []byte("hi")}); got != 8+3+2 {
		t.Errorf("frameWireSize = %d, want 13", got)
	}
}

// Occupancy gauges must exactly mirror live queue contents on every
// exit path: successful sends, a dead sender draining its backlog, and
// shutdown — otherwise operators alerting on queue depth see drift.
func TestFrameQueueGauges(t *testing.T) {
	// Success path: in on submit, out + noteToBridge after the send.
	m := NewMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan struct{}, 4)
	q := newFrameQueue(ctx, func(context.Context, proto.Frame) error {
		sent <- struct{}{}
		return nil
	}, m)
	if err := q.submit(proto.Frame{Payload: []byte("abcd")}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := m.queueFrames.Load(); got != 1 {
		t.Errorf("queueFrames after submit = %d, want 1", got)
	}
	<-sent
	waitFor(t, "gauge drains to zero after send", func() bool {
		return m.queueFrames.Load() == 0 && m.queueBytes.Load() == 0
	})
	if got := m.framesToBridges.Load(); got != 1 {
		t.Errorf("framesToBridges = %d, want 1", got)
	}

	// Dead-sender path: the first send fails, the sender keeps
	// draining (releasing each frame), so gauges return to zero even
	// though nothing was sent; late submits are then refused.
	m2 := NewMetrics()
	ctx2, cancel2 := context.WithCancel(context.Background())
	block := make(chan struct{})
	q2 := newFrameQueue(ctx2, func(context.Context, proto.Frame) error {
		<-block
		return errors.New("dead conn")
	}, m2)
	for _, n := range []int{10, 20, 30} {
		if err := q2.submit(proto.Frame{Payload: make([]byte, n)}); err != nil {
			t.Fatalf("submit %d: %v", n, err)
		}
	}
	if got := m2.queueBytes.Load(); got != 60 {
		t.Errorf("queueBytes with backlog = %d, want 60", got)
	}
	close(block) // unblock the sender; its send fails, backlog drains
	waitFor(t, "backlog released by dead sender", func() bool {
		return m2.queueFrames.Load() == 0 && m2.queueBytes.Load() == 0
	})
	if got := m2.framesToBridges.Load(); got != 0 {
		t.Errorf("framesToBridges on dead sender = %d, want 0", got)
	}
	// After shutdown, submit must refuse. Normally that is the ctx
	// error (checked first); ErrSendQueueClosed covers the race window
	// where closeAndDrain ran between submit's ctx check and its
	// acquisition of mu. Either way, nothing may enqueue behind a
	// queue that will never drain.
	cancel2()
	waitFor(t, "submit refused after shutdown", func() bool {
		return q2.submit(proto.Frame{}) != nil
	})
}

// End-to-end: the counters recorded on the live relay paths must match
// what actually crossed the gateway, and /metrics must serve them.
func TestMetricsRelayRoundTrip(t *testing.T) {
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

	// A bad-secret bridge attempt before the good session, so the
	// auth-failure counter is separable from the happy path.
	badConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("bad dial: %v", err)
	}
	sendHello(t, ctx, badConn, handshake.Hello{Role: "bridge", Name: "claude-laptop", Secret: "WRONG", TargetDaemon: "lab01"})
	if f, err := badConn.Recv(ctx); err != nil || f.Type != proto.FrameTypeError {
		t.Fatalf("bad bridge: %v %v", err, f.Type)
	}
	badConn.Close()
	if got := inst.Metrics.authFailures.Load(); got != 1 {
		t.Errorf("authFailures = %d, want 1", got)
	}

	daemonConn, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("daemon dial: %v", err)
	}
	defer daemonConn.Close()
	sendHello(t, ctx, daemonConn, handshake.Hello{Role: "daemon", Name: "lab01", Secret: "daemon-tok-1"})
	if ack, err := daemonConn.Recv(ctx); err != nil || ack.Type != proto.FrameTypeHelloAck {
		t.Fatalf("daemon hello ack: %v %v", err, ack.Type)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f, err := daemonConn.Recv(ctx)
		if err != nil || f.Type != proto.FrameTypeRoute {
			return
		}
		inner, err := proto.Decode(f.Payload)
		if err != nil {
			return
		}
		pong, _ := proto.Frame{Type: proto.FrameTypePong, ReqID: inner.ReqID}.Encode()
		_ = daemonConn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: pong})
	}()

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
	wg.Wait()

	m := inst.Metrics
	if got := m.daemonConnsActive.Load(); got != 1 {
		t.Errorf("daemonConnsActive = %d, want 1", got)
	}
	if got := m.bridgeConnsActive.Load(); got != 1 {
		t.Errorf("bridgeConnsActive = %d, want 1", got)
	}
	// bridge→daemon send is recorded synchronously before the frame
	// reaches the daemon, so it is already set once the daemon replied.
	if got := m.framesToDaemons.Load(); got != 1 {
		t.Errorf("framesToDaemons = %d, want 1", got)
	}
	if got := m.bytesToDaemons.Load(); got != int64(frameWireSize(proto.Frame{Type: proto.FrameTypeRoute, Payload: innerPing})) {
		t.Errorf("bytesToDaemons = %d, want inner size", got)
	}
	if got := m.framesFromDaemon.Load(); got != 1 {
		t.Errorf("framesFromDaemon = %d, want 1", got)
	}
	// The daemon→bridge counter is recorded by the queue sender after
	// the conn write succeeds; the bridge may observe the pong first.
	waitFor(t, "framesToBridges recorded", func() bool {
		return m.framesToBridges.Load() == 1 && m.bytesToBridges.Load() > 0
	})
	if got := m.queueFrames.Load(); got != 0 {
		t.Errorf("queueFrames after round trip = %d, want 0", got)
	}

	// /metrics must serve the live counters.
	addr, err := ServeMetrics(ctx, "127.0.0.1:0", m)
	if err != nil {
		t.Fatalf("ServeMetrics: %v", err)
	}
	httpResp, err := http.Get("http://" + addr.String() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer httpResp.Body.Close()
	body, _ := io.ReadAll(httpResp.Body)
	if ct := httpResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	for _, want := range []string{
		"agbridge_daemon_connections_active 1",
		"agbridge_frames_to_daemons_total 1",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
}
