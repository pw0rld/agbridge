package gateway

import (
	"context"
	"errors"
	"sync"

	"github.com/pw0rld/agbridge/internal/proto"
)

// DaemonProxy is the gateway-side handle for one online daemon. It carries
// the daemon's wss conn (for bridge→daemon writes) and the set of bridge
// writer subscriptions that handleBridge installs to receive daemon→bridge
// frames.
//
// The dconn read loop lives in handleDaemon: when a frame arrives it calls
// Deliver, which fans out to every live subscription and drops the frame
// if none exist. This decouples the daemon's read goroutine from any
// individual bridge session and eliminates the leak that used to happen
// when a bridge disconnected mid-stream.
//
// Why a subscription set instead of one swappable writer slot: with a
// single slot, an exiting bridge's unconditional clear could wipe the
// writer a *different* (or replacement) bridge had just installed,
// silently hanging that bridge until timeout — and a second distinct
// bridge could evict the first's writer outright. Each Subscribe gets a
// token; Unsubscribe removes exactly that subscription, never anyone
// else's. Fan-out to multiple subscribers is safe under e2e (foreign
// frames fail AEAD opening at the receiver); per-session demux for
// multi-bridge deployments is on the roadmap (see daemonState comment).
type DaemonProxy struct {
	conn connIO
	mu   sync.Mutex
	subs map[*bridgeWriter]struct{}
	// protoVersion is the Hello protocol generation the daemon
	// advertised at registration (0 = pre-versioning peer = generation 1).
	// handleBridge refuses to pair bridge and daemon across generations.
	protoVersion uint8
}

// bridgeWriter is one handleBridge subscription's token.
type bridgeWriter struct {
	name string
	fn   func(context.Context, proto.Frame) error
}

// NewDaemonProxy wraps an authenticated daemon conn. protoVersion is the
// generation from the daemon's Hello (handshake.DecodeHello yields 0 for
// pre-versioning peers; callers pass the decoded byte verbatim).
func NewDaemonProxy(c connIO, protoVersion uint8) *DaemonProxy {
	return &DaemonProxy{conn: c, protoVersion: protoVersion, subs: make(map[*bridgeWriter]struct{})}
}

// ProtoVersion returns the daemon's effective protocol generation.
func (p *DaemonProxy) ProtoVersion() uint8 {
	if p.protoVersion == 0 {
		return 1
	}
	return p.protoVersion
}

// Send forwards f to the daemon. Used by handleBridge's main loop.
func (p *DaemonProxy) Send(ctx context.Context, f proto.Frame) error {
	return p.conn.Send(ctx, f)
}

// Close closes the underlying daemon conn. Used by SessionTable.Revoke.
func (p *DaemonProxy) Close() error { return p.conn.Close() }

// Subscribe installs a daemon→bridge writer under bridge name and returns
// the token for Unsubscribe. Every subscription receives Deliver's frames
// until it is removed.
func (p *DaemonProxy) Subscribe(name string, fn func(context.Context, proto.Frame) error) *bridgeWriter {
	w := &bridgeWriter{name: name, fn: fn}
	p.mu.Lock()
	p.subs[w] = struct{}{}
	p.mu.Unlock()
	return w
}

// Unsubscribe removes exactly the subscription returned by Subscribe. A
// nil token is a no-op. Late calls (after the proxy died) are harmless.
func (p *DaemonProxy) Unsubscribe(w *bridgeWriter) {
	if w == nil {
		return
	}
	p.mu.Lock()
	delete(p.subs, w)
	p.mu.Unlock()
}

// Deliver fans a frame out from the daemon to every live subscription.
// Subscriptions whose send fails (bridge conn died) are dropped so they
// never linger after their handleBridge goroutine is gone. Drops the
// frame silently if no bridge is subscribed.
func (p *DaemonProxy) Deliver(ctx context.Context, f proto.Frame) error {
	p.mu.Lock()
	subs := make([]*bridgeWriter, 0, len(p.subs))
	for w := range p.subs {
		subs = append(subs, w)
	}
	p.mu.Unlock()
	var firstErr error
	for _, w := range subs {
		if err := w.fn(ctx, f); err != nil {
			p.Unsubscribe(w)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Registry tracks online daemons by name. Concurrent-safe.
type Registry struct {
	mu      sync.RWMutex
	daemons map[string]*DaemonProxy
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{daemons: make(map[string]*DaemonProxy)}
}

var ErrRegistryDuplicate = errors.New("gateway: daemon already registered with that name")

// Register adds proxy under name. Returns ErrRegistryDuplicate if name is taken.
func (r *Registry) Register(name string, p *DaemonProxy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.daemons[name]; exists {
		return ErrRegistryDuplicate
	}
	r.daemons[name] = p
	return nil
}

// Unregister removes name. No-op if absent.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.daemons, name)
}

// Lookup returns the proxy for name, ok=false if absent.
func (r *Registry) Lookup(name string) (*DaemonProxy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.daemons[name]
	return p, ok
}
