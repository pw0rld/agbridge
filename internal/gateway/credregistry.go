package gateway

import (
	"sync"

	"github.com/pw0rld/agbridge/internal/config"
)

// CredRegistry is the hot-swappable view of the gateway's allowed agents and
// daemons. Reads (LookupAgent / LookupDaemon) take an RLock; Replace swaps
// the underlying maps atomically. Use this for SIGHUP-driven config reload
// so in-flight handlers and Accept loops see a consistent snapshot without
// holding a write lock for the duration of a session.
type CredRegistry struct {
	mu      sync.RWMutex
	agents  map[string]config.AgentEntry
	daemons map[string]config.DaemonEntry
}

// NewCredRegistry builds the initial registry from cfg. Subsequent updates
// flow through Replace.
func NewCredRegistry(cfg *config.GatewayConfig) *CredRegistry {
	cr := &CredRegistry{}
	cr.Replace(cfg)
	return cr
}

// LookupAgent returns the AgentEntry for name, ok=false if absent.
func (cr *CredRegistry) LookupAgent(name string) (config.AgentEntry, bool) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	e, ok := cr.agents[name]
	return e, ok
}

// LookupDaemon returns the DaemonEntry for name, ok=false if absent.
func (cr *CredRegistry) LookupDaemon(name string) (config.DaemonEntry, bool) {
	cr.mu.RLock()
	defer cr.mu.RUnlock()
	e, ok := cr.daemons[name]
	return e, ok
}

// Replace swaps both maps under a single write lock so callers either see
// the entire old config or the entire new one.
func (cr *CredRegistry) Replace(cfg *config.GatewayConfig) {
	agents := make(map[string]config.AgentEntry, len(cfg.Agents))
	for _, a := range cfg.Agents {
		agents[a.Name] = a
	}
	daemons := make(map[string]config.DaemonEntry, len(cfg.Daemons))
	for _, d := range cfg.Daemons {
		daemons[d.Name] = d
	}
	cr.mu.Lock()
	cr.agents = agents
	cr.daemons = daemons
	cr.mu.Unlock()
}

// AddAgent inserts or replaces an agent entry. Used by the enroll
// handler to make a freshly-onboarded bridge immediately effective
// without requiring SIGHUP.
func (cr *CredRegistry) AddAgent(a config.AgentEntry) {
	cr.mu.Lock()
	if cr.agents == nil {
		cr.agents = map[string]config.AgentEntry{}
	}
	cr.agents[a.Name] = a
	cr.mu.Unlock()
}

// AddDaemon inserts or replaces a daemon entry.
func (cr *CredRegistry) AddDaemon(d config.DaemonEntry) {
	cr.mu.Lock()
	if cr.daemons == nil {
		cr.daemons = map[string]config.DaemonEntry{}
	}
	cr.daemons[d.Name] = d
	cr.mu.Unlock()
}
