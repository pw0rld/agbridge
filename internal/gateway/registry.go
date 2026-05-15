package gateway

import (
	"context"
	"errors"
	"sync"

	"github.com/pw0rld/agbridge/internal/proto"
)

// DaemonProxy is the gateway-side handle for one online daemon. It carries
// the daemon's wss conn (for bridge→daemon writes) and a swappable writer
// callback that handleBridge installs to receive daemon→bridge frames.
//
// The dconn read loop lives in handleDaemon: when a frame arrives it calls
// Deliver, which routes to the currently-installed writer or drops if no
// bridge is subscribed. This decouples the daemon's read goroutine from any
// individual bridge session and eliminates the leak that used to happen
// when a bridge disconnected mid-stream.
type DaemonProxy struct {
	conn connIO
	mu   sync.Mutex
	w    func(context.Context, proto.Frame) error
}

// NewDaemonProxy wraps an authenticated daemon conn.
func NewDaemonProxy(c connIO) *DaemonProxy {
	return &DaemonProxy{conn: c}
}

// Send forwards f to the daemon. Used by handleBridge's main loop.
func (p *DaemonProxy) Send(ctx context.Context, f proto.Frame) error {
	return p.conn.Send(ctx, f)
}

// Close closes the underlying daemon conn. Used by SessionTable.Revoke.
func (p *DaemonProxy) Close() error { return p.conn.Close() }

// SetWriter installs the daemon→bridge writer. Pass nil to clear (typically
// from handleBridge's defer when its session ends).
func (p *DaemonProxy) SetWriter(w func(context.Context, proto.Frame) error) {
	p.mu.Lock()
	p.w = w
	p.mu.Unlock()
}

// Deliver routes a frame from the daemon to the currently-installed writer.
// Drops the frame if no bridge is subscribed.
func (p *DaemonProxy) Deliver(ctx context.Context, f proto.Frame) error {
	p.mu.Lock()
	w := p.w
	p.mu.Unlock()
	if w == nil {
		return nil
	}
	return w(ctx, f)
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
