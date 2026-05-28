package cliclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

// maxBufferedOutput is the inline content cap for read_file and
// download (without dest_path). var (not const) so smoke tests can
// lower it; do not mutate at runtime in production.
var maxBufferedOutput int64 = 10 << 20

// errNoConn signals that no live connection is attached to the router —
// e.g. the dial path hasn't completed yet, or the supervisor cleared the
// conn in a reconnect window. Handlers translate this to network_lost.
var errNoConn = errors.New("cliclient: no active connection")

// Router is the bridge-side connection multiplexer used by Client.
// Each Invoke() builds one Router bound to one wss.Conn for the
// lifetime of that call. The runReader goroutine demuxes Route frames
// by ReqID into pending channels; handlers register a channel before
// they call sendInner and unregister on return.
//
// Mirrors the lowercase router in cmd/agbridge/bridge.go for now.
// Phase 4 (v0.0.2 finish) will delete that one and leave only this.
type Router struct {
	ctx       context.Context
	apiKey    []byte
	e2eMode   string         // "disabled" | "optional" | "required"
	bridgeKey *e2e.StaticKey // nil when e2e_mode == "disabled"
	daemonPub []byte         // nil when e2e_mode == "disabled"
	prologue  []byte         // cached IK prologue derived from gateway URL + target

	mu        sync.Mutex
	conn      *wss.Conn
	session   *e2e.Session // active E2E session (nil before handshake / after disconnect)
	sessionID [8]byte      // bound to session
	pending   map[string]chan proto.Frame

	// evicted is set when the gateway tells us a newer bridge with the
	// same identity has taken over (errFrame "session_replaced"). The
	// CLI variant has no supervisor loop, but we keep the flag and the
	// markIfEvicted/wasEvicted helpers so wire behaviour matches the
	// long-running bridge byte-for-byte.
	evicted atomic.Bool

	// sendMu serializes Encrypt + WSS Send so nonce consumption matches
	// wire order. noise.CipherState.Encrypt is not concurrent-safe, and
	// encrypting outside the WSS write lock would let goroutines reorder
	// ciphertexts on the wire — the receiver's monotone recv counter
	// would desync permanently. See internal/e2e/session_concurrent_test.go.
	sendMu sync.Mutex
}

// NewRouter constructs a Router pre-wired with the given conn (may be
// nil — installSession can attach later). When E2E is enabled, the
// bridge static key and daemon pubkey are loaded eagerly so the Noise
// IK call site can use them. Returns an error if E2E config is bad.
func NewRouter(ctx context.Context, conn *wss.Conn, apiKey []byte, cfg *config.BridgeConfig) (*Router, error) {
	r := &Router{
		ctx:     ctx,
		conn:    conn,
		apiKey:  apiKey,
		e2eMode: cfg.E2EMode,
		pending: make(map[string]chan proto.Frame),
	}
	if cfg.E2EMode != "disabled" {
		sk, err := e2e.LoadStatic(cfg.BridgeStaticKeyPath)
		if err != nil {
			return nil, fmt.Errorf("cliclient: load static key %q: %w", cfg.BridgeStaticKeyPath, err)
		}
		pub, err := e2e.PubFromBase64(cfg.DaemonPubkey)
		if err != nil {
			return nil, fmt.Errorf("cliclient: parse daemon_pubkey: %w", err)
		}
		r.bridgeKey = sk
		r.daemonPub = pub
		r.prologue = []byte(fmt.Sprintf("agbridge/v1|%s|%s", cfg.GatewayURL, cfg.TargetDaemon))
	}
	return r, nil
}

// markIfEvicted flips the evicted flag if f is the gateway's eviction
// signal. Returns true if it matched.
func (r *Router) markIfEvicted(f proto.Frame) bool {
	if f.Type == proto.FrameTypeError && string(f.Payload) == "session_replaced" {
		r.evicted.Store(true)
		return true
	}
	return false
}

// wasEvicted reports whether this router has ever been displaced by a
// newer bridge with the same identity. Kept for parity with the long-
// running bridge supervisor; the CLI doesn't loop on this signal.
func (r *Router) wasEvicted() bool { return r.evicted.Load() }

// currentConn returns the live wss.Conn under lock.
func (r *Router) currentConn() *wss.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

// replaceConn swaps in newConn and closes every pending call channel so
// the in-flight handler returns network_lost. The CLI Client never
// calls this in steady state (one-shot lifecycle), but keeping it
// preserves the surface area for tests that exercise reconnect paths.
func (r *Router) replaceConn(newConn *wss.Conn) {
	r.mu.Lock()
	old := r.pending
	r.pending = make(map[string]chan proto.Frame)
	r.conn = newConn
	// On disconnect, throw away E2E session keys (forward secrecy).
	r.session = nil
	r.sessionID = [8]byte{}
	r.mu.Unlock()
	for _, ch := range old {
		close(ch)
	}
}

// installSession attaches an E2E session to the current connection.
// Called by Client.dial after a successful Noise IK handshake.
func (r *Router) installSession(s *e2e.Session, sid [8]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.session = s
	r.sessionID = sid
}

// sendInner wraps inner as a Route frame and writes to the gateway.
// If an E2E session is active, the inner frame is AEAD-encrypted with
// AD = sessionID before HMAC signing. session_id is prepended to payload
// so the daemon can demultiplex its session map.
func (r *Router) sendInner(ctx context.Context, inner proto.Frame) error {
	innerBytes, err := inner.Encode()
	if err != nil {
		return err
	}

	// Encrypt + WSS Send must be a single critical section so nonce
	// consumption matches wire order. See sendMu comment on Router.
	r.sendMu.Lock()
	defer r.sendMu.Unlock()

	r.mu.Lock()
	sess := r.session
	sid := r.sessionID
	r.mu.Unlock()

	var routePayload []byte
	if sess != nil {
		ct, err := sess.Encrypt(sid[:], innerBytes)
		if err != nil {
			return fmt.Errorf("e2e encrypt: %w", err)
		}
		routePayload = make([]byte, 8+len(ct))
		copy(routePayload, sid[:])
		copy(routePayload[8:], ct)
	} else {
		routePayload = innerBytes
	}
	signed := auth.SignFrame(r.apiKey, routePayload)
	return r.send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed})
}

// decodeInbound parses a Route frame's payload, unwrapping AEAD when a
// session is active. Returns the inner Frame.
func (r *Router) decodeInbound(payload []byte) (proto.Frame, error) {
	r.mu.Lock()
	sess := r.session
	sid := r.sessionID
	r.mu.Unlock()

	if sess == nil {
		return proto.Decode(payload)
	}
	if len(payload) < 8 {
		return proto.Frame{}, errors.New("route payload too short for sid")
	}
	var got [8]byte
	copy(got[:], payload[:8])
	if got != sid {
		return proto.Frame{}, errors.New("route sid mismatch")
	}
	plain, err := sess.Decrypt(sid[:], payload[8:])
	if err != nil {
		return proto.Frame{}, fmt.Errorf("e2e decrypt: %w", err)
	}
	return proto.Decode(plain)
}

// send writes f via the current conn. Returns errNoConn if there's no
// live connection (in a reconnect window).
func (r *Router) send(ctx context.Context, f proto.Frame) error {
	conn := r.currentConn()
	if conn == nil {
		return errNoConn
	}
	return conn.Send(ctx, f)
}

// runReader dispatches every inner frame by its ReqID until the conn
// dies or ctx cancels. Caller (Client.dial) starts this goroutine after
// Hello+Noise complete; Client.Close cancels ctx to wind it down.
func (r *Router) runReader() {
	conn := r.currentConn()
	if conn == nil {
		return
	}
	for {
		f, err := conn.Recv(r.ctx)
		if err != nil {
			return
		}
		if r.markIfEvicted(f) {
			// Set the flag and keep reading; the gateway is about to
			// close the conn, so the next Recv will return.
			continue
		}
		if f.Type != proto.FrameTypeRoute {
			continue
		}
		inner, err := r.decodeInbound(f.Payload)
		if err != nil {
			continue
		}
		r.mu.Lock()
		ch, ok := r.pending[inner.ReqID]
		r.mu.Unlock()
		if ok {
			select {
			case ch <- inner:
			case <-r.ctx.Done():
				return
			}
		}
	}
}

func (r *Router) registerCall(reqID string) <-chan proto.Frame {
	ch := make(chan proto.Frame, 64)
	r.mu.Lock()
	r.pending[reqID] = ch
	r.mu.Unlock()
	return ch
}

func (r *Router) unregisterCall(reqID string) {
	r.mu.Lock()
	delete(r.pending, reqID)
	r.mu.Unlock()
}

// registerStream / unregisterStream are aliases reserved for future
// stream-style tools (port_forward family). Kept symmetric with the
// cmd/agbridge router so the porting story stays mechanical.
func (r *Router) registerStream(streamID string) <-chan proto.Frame {
	return r.registerCall(streamID)
}

func (r *Router) unregisterStream(streamID string) { r.unregisterCall(streamID) }

// mcpErrorResult builds the canonical MCP error result envelope
// (isError + content + _meta) used by every handler.
func mcpErrorResult(e errcode.Error) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []any{map[string]any{"type": "text", "text": e.Message}},
		"_meta":   metaToMap(e.ToMCPMeta()),
	}
}

func metaToMap(m errcode.Meta) map[string]any {
	return map[string]any{
		"error_code":     m.ErrorCode,
		"category":       m.Category,
		"retryable":      m.Retryable,
		"retry_after_ms": m.RetryAfterMs,
		"message":        m.Message,
	}
}

// newReqID returns a fresh 16-hex-char request ID for one tool call.
func newReqID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
