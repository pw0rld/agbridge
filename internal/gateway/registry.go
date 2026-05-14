package gateway

import (
	"errors"
	"sync"
)

// Registry tracks online daemons by name. Concurrent-safe.
type Registry struct {
	mu      sync.RWMutex
	daemons map[string]connIO
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{daemons: make(map[string]connIO)}
}

var ErrRegistryDuplicate = errors.New("gateway: daemon already registered with that name")

// Register adds c under name. Returns ErrRegistryDuplicate if name is taken.
func (r *Registry) Register(name string, c connIO) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.daemons[name]; exists {
		return ErrRegistryDuplicate
	}
	r.daemons[name] = c
	return nil
}

// Unregister removes name. No-op if absent.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.daemons, name)
}

// Lookup returns the conn for name, ok=false if absent.
func (r *Registry) Lookup(name string) (connIO, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.daemons[name]
	return c, ok
}
