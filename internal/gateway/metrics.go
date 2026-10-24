package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
)

// Metrics counts gateway activity for operators: connection gauges,
// per-direction relay counters, bridge send-queue occupancy, and
// failure counters that mirror the audit events worth alerting on.
// Everything is a lock-free atomic so the hot relay paths (daemon read
// loop, fan-out queue, bridge→daemon loop) can record without
// contending, and every recorder method is nil-safe: tests and
// embedded gateways may pass a nil *Metrics.
type Metrics struct {
	start time.Time

	daemonConnsActive atomic.Int64
	bridgeConnsActive atomic.Int64
	daemonConnsTotal  atomic.Int64
	bridgeConnsTotal  atomic.Int64

	framesFromDaemon atomic.Int64
	bytesFromDaemon  atomic.Int64
	framesToDaemons  atomic.Int64
	bytesToDaemons   atomic.Int64
	framesToBridges  atomic.Int64
	bytesToBridges   atomic.Int64

	// Bridge fan-out queue occupancy summed over all live queues.
	queueFrames atomic.Int64
	queueBytes  atomic.Int64

	authFailures       atomic.Int64
	authzFailures      atomic.Int64
	macFailures        atomic.Int64
	protocolMismatches atomic.Int64
	sessionsReplaced   atomic.Int64
	queueOverflows     atomic.Int64
}

// NewMetrics returns a zeroed Metrics with the uptime clock started.
func NewMetrics() *Metrics {
	return &Metrics{start: time.Now()}
}

// frameWireSize is the on-the-wire size of an encoded frame: the fixed
// 8-byte header (version|type|reqid_len:2B|payload_len:4B) plus the
// ReqID and payload. Cheap proxy for "bytes relayed" that avoids a
// full Encode on the hot path.
func frameWireSize(f proto.Frame) int {
	return 8 + len(f.ReqID) + len(f.Payload)
}

// --- connection gauges -------------------------------------------------

func (m *Metrics) daemonConnected() {
	if m == nil {
		return
	}
	m.daemonConnsActive.Add(1)
	m.daemonConnsTotal.Add(1)
}

func (m *Metrics) daemonDisconnected() {
	if m == nil {
		return
	}
	m.daemonConnsActive.Add(-1)
}

func (m *Metrics) bridgeConnected() {
	if m == nil {
		return
	}
	m.bridgeConnsActive.Add(1)
	m.bridgeConnsTotal.Add(1)
}

func (m *Metrics) bridgeDisconnected() {
	if m == nil {
		return
	}
	m.bridgeConnsActive.Add(-1)
}

// --- relay counters ----------------------------------------------------

// noteDaemonInbound records one frame read off a daemon connection.
func (m *Metrics) noteDaemonInbound(n int) {
	if m == nil {
		return
	}
	m.framesFromDaemon.Add(1)
	m.bytesFromDaemon.Add(int64(n))
}

// noteToDaemon records one frame successfully written toward a daemon.
func (m *Metrics) noteToDaemon(n int) {
	if m == nil {
		return
	}
	m.framesToDaemons.Add(1)
	m.bytesToDaemons.Add(int64(n))
}

// noteToBridge records one frame successfully written toward a bridge
// (after the queue sender confirms the write, so the counter reflects
// bytes actually handed to the transport).
func (m *Metrics) noteToBridge(n int) {
	if m == nil {
		return
	}
	m.framesToBridges.Add(1)
	m.bytesToBridges.Add(int64(n))
}

// --- bridge send-queue occupancy ---------------------------------------

// noteQueueIn records a frame accepted into a bridge fan-out queue.
func (m *Metrics) noteQueueIn(n int) {
	if m == nil {
		return
	}
	m.queueFrames.Add(1)
	m.queueBytes.Add(int64(n))
}

// noteQueueOut records a frame leaving a queue, whether it was sent,
// dropped as the sender died, or drained at shutdown — occupancy can
// only go down here, so gauges cannot drift.
func (m *Metrics) noteQueueOut(n int) {
	if m == nil {
		return
	}
	m.queueFrames.Add(-1)
	m.queueBytes.Add(-int64(n))
}

// --- failure counters (mirror audit events) ------------------------------

func (m *Metrics) authFailed() {
	if m == nil {
		return
	}
	m.authFailures.Add(1)
}

func (m *Metrics) authzFailed() {
	if m == nil {
		return
	}
	m.authzFailures.Add(1)
}

func (m *Metrics) macFailed() {
	if m == nil {
		return
	}
	m.macFailures.Add(1)
}

func (m *Metrics) protocolMismatch() {
	if m == nil {
		return
	}
	m.protocolMismatches.Add(1)
}

func (m *Metrics) sessionReplaced() {
	if m == nil {
		return
	}
	m.sessionsReplaced.Add(1)
}

func (m *Metrics) queueOverflow() {
	if m == nil {
		return
	}
	m.queueOverflows.Add(1)
}

// Render writes the current values in Prometheus text exposition
// format (text/plain; version=0.0.4). Gauges are rendered as plain
// gauges, cumulative counters with the _total suffix per convention.
func (m *Metrics) Render(w io.Writer) {
	if m == nil {
		return
	}
	metric := func(name, help, kind string, v int64) {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, kind, name, v)
	}
	metric("agbridge_uptime_seconds", "Gateway uptime in seconds.", "gauge", int64(time.Since(m.start).Seconds()))
	metric("agbridge_daemon_connections_active", "Daemon connections currently established.", "gauge", m.daemonConnsActive.Load())
	metric("agbridge_daemon_connections_total", "Daemon connections accepted since start.", "counter", m.daemonConnsTotal.Load())
	metric("agbridge_bridge_connections_active", "Bridge connections currently established.", "gauge", m.bridgeConnsActive.Load())
	metric("agbridge_bridge_connections_total", "Bridge connections accepted since start.", "counter", m.bridgeConnsTotal.Load())
	metric("agbridge_frames_from_daemon_total", "Frames received from daemons.", "counter", m.framesFromDaemon.Load())
	metric("agbridge_bytes_from_daemon_total", "Wire bytes received from daemons.", "counter", m.bytesFromDaemon.Load())
	metric("agbridge_frames_to_daemons_total", "Frames written to daemons.", "counter", m.framesToDaemons.Load())
	metric("agbridge_bytes_to_daemons_total", "Wire bytes written to daemons.", "counter", m.bytesToDaemons.Load())
	metric("agbridge_frames_to_bridges_total", "Frames written to bridges.", "counter", m.framesToBridges.Load())
	metric("agbridge_bytes_to_bridges_total", "Wire bytes written to bridges.", "counter", m.bytesToBridges.Load())
	metric("agbridge_bridge_queue_frames", "Frames currently queued for bridge delivery.", "gauge", m.queueFrames.Load())
	metric("agbridge_bridge_queue_bytes", "Payload bytes currently queued for bridge delivery.", "gauge", m.queueBytes.Load())
	metric("agbridge_auth_failures_total", "Authentication failures (bad token/key).", "counter", m.authFailures.Load())
	metric("agbridge_authz_failures_total", "Authorization failures (target not allowed).", "counter", m.authzFailures.Load())
	metric("agbridge_mac_failures_total", "E2E frame MAC verification failures.", "counter", m.macFailures.Load())
	metric("agbridge_protocol_mismatches_total", "Frames rejected for protocol version mismatch.", "counter", m.protocolMismatches.Load())
	metric("agbridge_session_replaced_total", "Sessions replaced by a newer connection with the same identity.", "counter", m.sessionsReplaced.Load())
	metric("agbridge_bridge_queue_overflows_total", "Bridge connections killed for send-queue overflow.", "counter", m.queueOverflows.Load())
}

// ServeMetrics starts a localhost-only HTTP server exposing /metrics in
// Prometheus text format and shuts it down when ctx is cancelled. An
// empty addr disables the listener entirely (returns nil without
// starting anything); callers gate on config, this guards accidental
// empty strings. Bind failures are returned to the caller so the
// gateway can fail fast on a misconfigured metrics address. The
// returned Addr is the actual bound address (useful when addr uses
// port 0 in tests).
func ServeMetrics(ctx context.Context, addr string, m *Metrics) (net.Addr, error) {
	if addr == "" {
		return nil, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.Render(w)
	})
	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	return ln.Addr(), nil
}
