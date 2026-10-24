package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/agentsock"
	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newAgentCmd implements `agbridge agent`: an optional local broker
// that keeps gateway connections (TCP+TLS+WS+Hello+Noise) hot so
// one-shot CLI invocations skip the handshake. See
// docs/design/agent-connection-reuse.md.
func newAgentCmd() *cobra.Command {
	var (
		socket      string
		idleTimeout time.Duration
		exitAfter   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run the local connection-reuse broker (foreground)",
		Long: "Keeps one hot connection per gateway+target so repeated CLI commands\n" +
			"skip the full TCP+TLS+WS+Hello+Noise handshake. The CLI uses the\n" +
			"broker automatically whenever the socket exists; without an agent\n" +
			"everything behaves exactly as before (one-shot dials).\n\n" +
			"Env: AGBRIDGE_AGENT_SOCKET=<path|off>, AGBRIDGE_NO_AGENT=1.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if socket == "" {
				socket = agentSocketPath()
				if socket == "" {
					return errors.New("agent: socket path disabled via env; unset AGBRIDGE_NO_AGENT/AGBRIDGE_AGENT_SOCKET=off first")
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			pool := &agentPool{entries: map[string]*agentPoolEntry{}, dial: agentDial, idleTimeout: idleTimeout}
			go pool.gcLoop(ctx)

			fmt.Fprintf(cmd.ErrOrStderr(), "agbridge agent: listening on %s\n", socket)
			opts := agentsock.ServerOptions{IdleTimeout: idleTimeout}
			if exitAfter > 0 {
				opts.OnIdle = func(last time.Time) error {
					if time.Since(last) > exitAfter {
						fmt.Fprintf(os.Stderr, "agbridge agent: idle > %s, exiting\n", exitAfter)
						return errors.New("idle exit")
					}
					return nil
				}
			}
			err := agentsock.Serve(ctx, socket, pool.handle, opts)
			pool.closeAll()
			return err
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "UDS path (default: agent.sock under the state dir)")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", 15*time.Minute, "close pooled connections idle longer than this (0 = never)")
	cmd.Flags().DurationVar(&exitAfter, "exit-after", 60*time.Minute, "exit the agent itself after this much inactivity (0 = run forever)")

	status := &cobra.Command{
		Use:   "status",
		Short: "Query the running agent broker",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
			defer cancel()
			st, handled, err := agentStatus(ctx, agentSocketPath())
			if !handled {
				printErrorJSON(cmd.ErrOrStderr(), "agent_unavailable", "no agent broker on "+agentSocketPath(), false)
				return fmt.Errorf("no agent broker on %s", agentSocketPath())
			}
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "agent_error", err.Error(), false)
				return err
			}
			printJSON(cmd.OutOrStdout(), st)
			return nil
		},
	}
	cmd.AddCommand(status)
	return cmd
}

// agentCaller is the pooled resource: a live handshook connection.
// *cliclient.Client satisfies it; tests inject fakes.
type agentCaller interface {
	Dispatch(ctx context.Context, name string, args json.RawMessage) (any, error)
	Close() error
}

// agentDial is the production dialer (test seam).
var agentDial = func(ctx context.Context, opts cliclient.Options) (agentCaller, error) {
	return cliclient.Connect(ctx, opts)
}

// agentPool keeps at most one live connection per dial identity
// (gateway + agent + target + crypto parameters). Entries are borrowed
// for the whole duration of a dispatched request: entry.mu serialises
// requests per connection, while different targets never block each
// other. API keys are deliberately NOT part of the key: a rotated key
// keeps working on the already-authenticated connection, and the next
// dial-on-miss picks up the new credential.
type agentPool struct {
	mu          sync.Mutex
	entries     map[string]*agentPoolEntry
	dial        func(ctx context.Context, opts cliclient.Options) (agentCaller, error)
	idleTimeout time.Duration
}

type agentPoolEntry struct {
	key     string
	gateway string
	target  string

	// mu guards caller/removed. acquire() returns with mu HELD; the
	// borrower releases via release(). Concurrent requests for the
	// same target therefore queue behind the in-flight one — same as
	// if they each dialed their own connection, minus the handshakes.
	mu      sync.Mutex
	caller  agentCaller
	removed bool

	// lastUsed is atomic so status()/gcOnce can read it without
	// blocking behind a borrowed entry.
	lastUsed atomic.Int64
}

func poolKey(o cliclient.Options) string {
	return strings.Join([]string{
		o.GatewayURL, o.AgentName, o.Target,
		o.E2EMode, o.GatewayTLSMode, o.CertPin,
		o.BridgeStaticKeyPath, o.DaemonPubkey,
	}, "\x00")
}

// acquire borrows a live caller for opts, dialing on cache miss under
// entry.mu (concurrent misses for the same key coalesce into one
// dial). On success the entry is returned LOCKED; the caller must
// release() it. Dial failure evicts the entry so the next request
// retries from scratch.
func (p *agentPool) acquire(ctx context.Context, opts cliclient.Options) (*agentPoolEntry, error) {
	key := poolKey(opts)
	for {
		p.mu.Lock()
		e, ok := p.entries[key]
		if !ok {
			e = &agentPoolEntry{key: key, gateway: opts.GatewayURL, target: opts.Target}
			p.entries[key] = e
		}
		p.mu.Unlock()

		e.mu.Lock()
		if e.removed {
			// Another borrower evicted us mid-flight; loop to get
			// (or create) a fresh entry under the same key.
			e.mu.Unlock()
			continue
		}
		if e.caller == nil {
			caller, err := p.dial(ctx, opts)
			if err != nil {
				e.removed = true
				p.removeLocked(e)
				e.mu.Unlock()
				return nil, err
			}
			e.caller = caller
		}
		e.lastUsed.Store(time.Now().UnixNano())
		return e, nil
	}
}

// release returns a borrowed entry (acquire's counterpart).
func (e *agentPoolEntry) release() { e.mu.Unlock() }

// removeLocked drops e from the map if it is still the current entry
// for its key. Takes only p.mu, safe from any context.
func (p *agentPool) removeLocked(e *agentPoolEntry) {
	p.mu.Lock()
	if p.entries[e.key] == e {
		delete(p.entries, e.key)
	}
	p.mu.Unlock()
}

// dropLocked tears down e's caller and removes it from the pool.
// The caller MUST hold e.mu (the borrower closes its own connection).
func (p *agentPool) dropLocked(e *agentPoolEntry) {
	if e.caller != nil {
		_ = e.caller.Close()
		e.caller = nil
	}
	e.removed = true
	p.removeLocked(e)
}

// handle is the agentsock.Handler: reserved control names are served
// locally, everything else is dispatched through the pooled client.
func (p *agentPool) handle(ctx context.Context, req agentsock.Request) (any, error) {
	if req.Handler == "__status__" {
		return p.status(), nil
	}
	var opts cliclient.Options
	if len(req.Opts) == 0 {
		return nil, errors.New("agent: request missing opts")
	}
	if err := json.Unmarshal(req.Opts, &opts); err != nil {
		return nil, fmt.Errorf("agent: bad opts: %w", err)
	}
	args := req.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}

	e, err := p.acquire(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer e.release()

	if req.Handler == "__connect__" {
		return map[string]any{"connected": true}, nil
	}

	res, err := e.caller.Dispatch(ctx, req.Handler, args)
	if err == nil && isNetworkLost(res) {
		// The connection died mid-request. Surface the error to the
		// CLI (never retry — side effects may have landed) but drop
		// the corpse so the next request gets a fresh dial.
		p.dropLocked(e)
	}
	return res, err
}

// isNetworkLost detects the MCP error envelope the handlers produce
// when the underlying connection dies: {"isError":true,
// "_meta":{"error_code":"network_lost",...}}.
func isNetworkLost(res any) bool {
	m, ok := res.(map[string]any)
	if !ok || m["isError"] != true {
		return false
	}
	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		return false
	}
	return meta["error_code"] == "network_lost"
}

func (p *agentPool) status() map[string]any {
	p.mu.Lock()
	items := make([]map[string]any, 0, len(p.entries))
	for _, e := range p.entries {
		idleMs := (time.Now().UnixNano() - e.lastUsed.Load()) / int64(time.Millisecond)
		items = append(items, map[string]any{
			"gateway": e.gateway,
			"target":  e.target,
			"idle_ms": idleMs,
		})
	}
	p.mu.Unlock()
	return map[string]any{"connections": len(items), "entries": items}
}

// gcLoop closes entries idle beyond idleTimeout, running until ctx is
// cancelled. idleTimeout <= 0 disables collection entirely.
func (p *agentPool) gcLoop(ctx context.Context) {
	if p.idleTimeout <= 0 {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.gcOnce()
		}
	}
}

func (p *agentPool) gcOnce() {
	cutoff := time.Now().Add(-p.idleTimeout).UnixNano()
	for _, e := range p.snapshot() {
		e.mu.Lock()
		if !e.removed && e.lastUsed.Load() < cutoff {
			p.dropLocked(e)
		}
		e.mu.Unlock()
	}
}

// closeAll drops every pooled connection. Safe to call multiple times
// (dropLocked is nil-safe); called when Serve returns.
func (p *agentPool) closeAll() {
	for _, e := range p.snapshot() {
		e.mu.Lock()
		p.dropLocked(e)
		e.mu.Unlock()
	}
}

func (p *agentPool) snapshot() []*agentPoolEntry {
	p.mu.Lock()
	entries := make([]*agentPoolEntry, 0, len(p.entries))
	for _, e := range p.entries {
		entries = append(entries, e)
	}
	p.mu.Unlock()
	return entries
}
