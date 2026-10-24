// Package agentsock implements the local Unix-domain-socket protocol
// between the one-shot agbridge CLI and a long-running `agbridge agent`
// connection broker. See docs/design/agent-connection-reuse.md.
//
// Wire format: one JSON object per line (NDJSON) in each direction.
// The transport is pure framing + framing-level error mapping; all
// semantics (connection pools, handler dispatch) live with the caller.
package agentsock

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"
)

// DefaultConnectTimeout bounds the CLI-side socket connect. The socket
// either exists (agent up) or it doesn't; anything slower than this is
// treated as absent so the CLI falls back to a one-shot dial fast.
const DefaultConnectTimeout = 200 * time.Millisecond

// Request is one CLI → agent message. Opts carries the marshalled
// cliclient.Options the agent should dial with when it has no pooled
// connection for this (gateway, target). Handler is the tool handler
// name (see cliclient.Router.Dispatch); the reserved names "__connect__"
// and "__status__" are handled by the server without touching a pool.
type Request struct {
	ID        string          `json:"id"`
	Handler   string          `json:"handler"`
	Args      json.RawMessage `json:"args,omitempty"`
	Opts      json.RawMessage `json:"opts,omitempty"`
	TimeoutMs int64           `json:"timeout_ms,omitempty"`
}

// Response is one agent → CLI message. Tool-level failures travel as
// OK=true with the MCP isError envelope inside Result; OK=false is
// reserved for agent/plumbing errors (dial failures, unknown handler,
// timeouts) and the Error text mirrors what the one-shot path would
// have produced.
type Response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// NewID returns a fresh 16-hex-char request id.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ErrAgentGone signals the agent closed the connection before answering
// (shutdown race). Callers treat it exactly like "socket absent": the
// request was demonstrably not answered, and a retry via either path
// is safe ONLY for idempotent probes — the client surfaces it and lets
// the caller decide.
var ErrAgentGone = errors.New("agentsock: agent closed connection before response")

// Dial connects to the socket with a bounded timeout. Any error here
// means "no agent, use the classic path".
func Dial(socketPath string) (net.Conn, error) {
	d := net.Dialer{Timeout: DefaultConnectTimeout}
	conn, err := d.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		if err := uc.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return conn, error(nil)
}

// Call sends req and waits for the matching response. Once the request
// has been fully written, errors do NOT imply "not executed": the
// caller must not fall back to the one-shot path (double side effects).
// handled reports whether the request made it onto the wire.
func Call(ctx context.Context, conn net.Conn, req Request) (resp Response, handled bool, err error) {
	line, err := json.Marshal(req)
	if err != nil {
		return Response{}, false, err
	}
	if wc, ok := conn.(interface{ SetWriteDeadline(t time.Time) error }); ok {
		_ = wc.SetWriteDeadline(deadlineFrom(ctx))
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return Response{}, false, fmt.Errorf("agentsock: write: %w", err)
	}
	handled = true

	// Read side: honor ctx via per-read deadlines.
	if rc, ok := conn.(interface{ SetReadDeadline(t time.Time) error }); ok {
		_ = rc.SetReadDeadline(deadlineFrom(ctx))
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20) // responses can be large (e.g. read_file)
	if !sc.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, true, ctxErr
		}
		if err := sc.Err(); err != nil {
			return Response{}, true, fmt.Errorf("agentsock: read: %w", err)
		}
		return Response{}, true, ErrAgentGone
	}
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return Response{}, true, fmt.Errorf("agentsock: decode response: %w", err)
	}
	if resp.ID != req.ID {
		return Response{}, true, fmt.Errorf("agentsock: response id mismatch")
	}
	return resp, true, nil
}

func deadlineFrom(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Now().Add(24 * time.Hour)
}

// Handler serves one request. Result is marshalled into Response.Result;
// returning an error produces Response{OK:false, Error:err.Error()}.
type Handler func(ctx context.Context, req Request) (result any, err error)

// ServerOptions tunes Serve. Zero values: IdleTimeout 15m.
type ServerOptions struct {
	// IdleTimeout closes pooled resources that have not been used.
	// The agent command implements this via the Handler's OnIdle hook
	// below; Server itself only tracks per-connection lifetime.
	IdleTimeout time.Duration
	// OnIdle is invoked from the Serve loop every minute with the time
	// of the last served request; returning an error stops Serve.
	OnIdle func(lastActivity time.Time) error
}

// Serve listens on socketPath (created 0600 inside a 0700 dir),
// serving one request per line until ctx cancels. Multiple concurrent
// connections are handled independently. On return (error or ctx
// cancel) the socket file is removed.
func Serve(ctx context.Context, socketPath string, h Handler, opts ServerOptions) error {
	if err := os.MkdirAll(dirOf(socketPath), 0o700); err != nil {
		return fmt.Errorf("agentsock: mkdir: %w", err)
	}
	// Stale socket from a crashed agent: try to unlink once.
	if _, err := os.Stat(socketPath); err == nil {
		_ = os.Remove(socketPath)
	}
	ucaddr := &net.UnixAddr{Name: socketPath, Net: "unix"}
	ln, err := net.ListenUnix("unix", ucaddr)
	if err != nil {
		return fmt.Errorf("agentsock: listen: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(socketPath)
		return fmt.Errorf("agentsock: chmod: %w", err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var lastActivityUnix atomic.Int64
	lastActivityUnix.Store(time.Now().Unix())
	touch := func() { lastActivityUnix.Store(time.Now().Unix()) }
	if opts.OnIdle != nil {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					if err := opts.OnIdle(time.Unix(lastActivityUnix.Load(), 0)); err != nil {
						_ = ln.Close()
						return
					}
				}
			}
		}()
	}

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("agentsock: accept: %w", err)
		}
		go func() {
			defer conn.Close()
			sc := bufio.NewScanner(conn)
			sc.Buffer(make([]byte, 0, 16<<10), 1<<20)
			for sc.Scan() {
				var req Request
				if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
					writeResp(conn, Response{Error: "agentsock: bad request: " + err.Error()})
					continue
				}
				touch()
				rctx := ctx
				if req.TimeoutMs > 0 {
					var cancel context.CancelFunc
					rctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
					result, err := h(rctx, req)
					cancel()
					finish(conn, req, result, err)
					continue
				}
				result, err := h(rctx, req)
				finish(conn, req, result, err)
			}
		}()
	}
}

func finish(conn net.Conn, req Request, result any, err error) {
	if err != nil {
		writeResp(conn, Response{ID: req.ID, OK: false, Error: err.Error()})
		return
	}
	raw, merr := json.Marshal(result)
	if merr != nil {
		writeResp(conn, Response{ID: req.ID, OK: false, Error: "agentsock: marshal result: " + merr.Error()})
		return
	}
	writeResp(conn, Response{ID: req.ID, OK: true, Result: raw})
}

func writeResp(conn net.Conn, r Response) {
	line, err := json.Marshal(r)
	if err != nil {
		line = []byte(`{"ok":false,"error":"agentsock: response marshal failed"}`)
	}
	_, _ = conn.Write(append(line, '\n'))
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/"
			}
			return p[:i]
		}
	}
	return "."
}
