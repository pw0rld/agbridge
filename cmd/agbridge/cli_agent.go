package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pw0rld/agbridge/internal/agentsock"
	"github.com/pw0rld/agbridge/internal/cliclient"
)

// agentSocketPath resolves the broker socket for this invocation:
//
//	AGBRIDGE_NO_AGENT=1          → disabled (empty)
//	AGBRIDGE_AGENT_SOCKET=<path> → explicit path ("off" disables)
//	default                      → <state-dir>/agent.sock
//
// The socket's existence is the opt-in: when no agent is listening the
// CLI behaves exactly as before (one-shot dial).
func agentSocketPath() string {
	if os.Getenv("AGBRIDGE_NO_AGENT") == "1" {
		return ""
	}
	if v := os.Getenv("AGBRIDGE_AGENT_SOCKET"); v != "" {
		if v == "off" {
			return ""
		}
		return v
	}
	return filepath.Join(filepath.Dir(stateJSONPath(g.stateDir)), "agent.sock")
}

// invokeRouted runs a named tool handler, preferring the local agent
// broker when its socket is reachable and falling back — only when the
// request demonstrably never left the process — to a full one-shot
// dial. Once request bytes are on the agent socket we never retry: the
// daemon may have already applied side effects.
func invokeRouted(ctx context.Context, opts cliclient.Options, handler string, args map[string]any) (any, error) {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("encode args: %w", err)
		}
		raw = b
	} else {
		raw = json.RawMessage(`{}`)
	}
	if res, handled, err := agentCall(ctx, opts, handler, raw); handled {
		return res, err
	}
	return cliclient.InvokeHandler(ctx, opts, handler, raw)
}

// ensureConnected performs (or reuses) a full connect without invoking
// a tool handler — backs `agbridge status`.
func ensureConnected(ctx context.Context, opts cliclient.Options) error {
	_, handled, err := agentCall(ctx, opts, "__connect__", json.RawMessage(`{}`))
	if handled {
		return err
	}
	_, err = cliclient.Invoke(ctx, opts, func(context.Context, *cliclient.Client) (any, error) {
		return nil, nil
	})
	return err
}

// agentCall attempts one broker round-trip. handled=false means "no
// agent available, classic path" (socket disabled/absent/connect or
// pre-write failure). handled=true means the answer (or a transport
// error after the request went out) is final.
func agentCall(ctx context.Context, opts cliclient.Options, handler string, args json.RawMessage) (res any, handled bool, err error) {
	sock := opts.AgentSocket
	if sock == "" {
		return nil, false, nil
	}
	conn, derr := agentsock.Dial(sock)
	if derr != nil {
		return nil, false, nil // no agent: fall back
	}
	defer conn.Close()

	// The agent dials on our behalf: hand it the full Options. Same
	// trust domain by construction (0600 socket, same-user state dir).
	optsJSON, merr := json.Marshal(opts)
	if merr != nil {
		return nil, false, nil
	}
	req := agentsock.Request{
		ID:      agentsock.NewID(),
		Handler: handler,
		Args:    args,
		Opts:    optsJSON,
	}
	if d, ok := ctx.Deadline(); ok {
		req.TimeoutMs = int64(time.Until(d).Milliseconds())
		if req.TimeoutMs < 0 {
			req.TimeoutMs = 1
		}
	}
	resp, handled, cerr := agentsock.Call(ctx, conn, req)
	if handled && errors.Is(cerr, agentsock.ErrAgentGone) {
		// Agent vanished mid-flight. The request may already have run
		// daemon-side; surfacing the error beats a duplicate.
		return nil, true, fmt.Errorf("agent: connection lost: %w", cerr)
	}
	if cerr != nil {
		return nil, handled, cerr
	}
	if !resp.OK {
		// Agent-side plumbing error (dial failure, unknown handler,
		// timeout). Mirror the one-shot error shape for the CLI.
		return nil, true, errors.New(resp.Error)
	}
	var out any
	if len(resp.Result) > 0 && string(resp.Result) != "null" {
		if uerr := json.Unmarshal(resp.Result, &out); uerr != nil {
			return nil, true, fmt.Errorf("agent: bad result: %w", uerr)
		}
	}
	return out, true, nil
}

// agentStatus asks the broker (when present) for its pool snapshot.
// Returns handled=false when no agent is reachable.
func agentStatus(ctx context.Context, sock string) (map[string]any, bool, error) {
	if sock == "" {
		return nil, false, nil
	}
	conn, err := agentsock.Dial(sock)
	if err != nil {
		return nil, false, nil
	}
	defer conn.Close()
	resp, handled, err := agentsock.Call(ctx, conn, agentsock.Request{ID: agentsock.NewID(), Handler: "__status__"})
	if !handled {
		return nil, false, err
	}
	if err != nil {
		return nil, true, err
	}
	if !resp.OK {
		return nil, true, errors.New(resp.Error)
	}
	var out map[string]any
	if uerr := json.Unmarshal(resp.Result, &out); uerr != nil {
		return nil, true, uerr
	}
	return out, true, nil
}
