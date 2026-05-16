package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/dialer"
	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/mcp"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/streamproto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

const maxBufferedOutput = 10 << 20

var errNoConn = errors.New("bridge: no active connection")

func newBridgeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Run the local MCP bridge",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadBridge(cfgPath)
			if err != nil {
				return err
			}
			tlsCfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
			if err := auth.AttachCertPin(tlsCfg, cfg.CertPin); err != nil {
				return fmt.Errorf("cert pin: %w", err)
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			rt, err := newRouter(ctx, nil, []byte(cfg.APIKey), cfg)
			if err != nil {
				return err
			}

			// First connect synchronously so we never serve MCP before an
			// initial handshake succeeds — otherwise the very first tool call
			// would observe network_lost. If handshake fails we return the
			// error from RunE; main() exits non-zero. Installing the
			// signal-driven os.Exit goroutine BEFORE this point would race
			// with the deferred cancel() and force exit 0, masking failures.
			if err := bridgeDialAndAttach(ctx, rt, cfg, tlsCfg); err != nil {
				return fmt.Errorf("initial handshake: %w", err)
			}
			fmt.Fprintln(os.Stderr, "bridge: handshake ok, starting MCP stdio server")

			// On SIGTERM/SIGINT, exit immediately. os.Stdin.Close() does not
			// reliably wake bufio.Scanner's blocked Read on Linux (stdin is
			// not in pollable mode), so a soft shutdown is impractical.
			// The bridge has no shared state beyond the WSS conn, which the
			// gateway will detect-and-tear-down on disconnect.
			go func() {
				<-ctx.Done()
				fmt.Fprintln(os.Stderr, "bridge: shutdown signal received")
				os.Exit(0)
			}()

			go superviseBridgeConn(ctx, rt, cfg, tlsCfg)

			srv := mcp.NewServer()
			srv.RegisterTool(mcp.ToolSpec{
				Name:        "exec",
				Description: "Run a command on the remote daemon machine and return stdout/stderr/exitcode.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"cmd":        map[string]any{"type": "string"},
						"args":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"cwd":        map[string]any{"type": "string"},
						"env":        map[string]any{"type": "object"},
						"timeout_ms": map[string]any{"type": "integer"},
					},
					"required": []string{"cmd", "cwd"},
				},
			}, rt.execHandler)

			srv.RegisterTool(mcp.ToolSpec{
				Name:        "read_file",
				Description: "Read a file on the remote daemon machine. Returns content + sha256 in _meta.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":     map[string]any{"type": "string"},
						"max_size": map[string]any{"type": "integer"},
					},
					"required": []string{"path"},
				},
			}, rt.readFileHandler)

			srv.RegisterTool(mcp.ToolSpec{
				Name:        "write_file",
				Description: "Write a file on the remote daemon machine. content_b64 is base64-encoded content.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string"},
						"content_b64": map[string]any{"type": "string"},
						"mode":        map[string]any{"type": "integer"},
					},
					"required": []string{"path", "content_b64"},
				},
			}, rt.writeFileHandler)

			srv.RegisterTool(mcp.ToolSpec{
				Name:        "port_forward",
				Description: "Bind a local TCP listener that forwards new connections to remote_host:remote_port on the daemon machine.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"remote_host": map[string]any{"type": "string"},
						"remote_port": map[string]any{"type": "integer"},
						"local_port":  map[string]any{"type": "integer"},
					},
					"required": []string{"remote_host", "remote_port"},
				},
			}, rt.portForwardHandler)

			err = srv.Serve(ctx, os.Stdin, os.Stdout)
			// Closed-fd errors from SIGTERM-triggered stdin close are
			// expected; treat them as graceful shutdown.
			if ctx.Err() != nil {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to bridge YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

type router struct {
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
}

func newRouter(ctx context.Context, conn *wss.Conn, apiKey []byte, cfg *config.BridgeConfig) (*router, error) {
	r := &router{
		ctx:     ctx,
		conn:    conn,
		apiKey:  apiKey,
		e2eMode: cfg.E2EMode,
		pending: make(map[string]chan proto.Frame),
	}
	if cfg.E2EMode != "disabled" {
		sk, err := e2e.LoadStatic(cfg.BridgeStaticKeyPath)
		if err != nil {
			return nil, fmt.Errorf("bridge: load static key %q: %w", cfg.BridgeStaticKeyPath, err)
		}
		pub, err := e2e.PubFromBase64(cfg.DaemonPubkey)
		if err != nil {
			return nil, fmt.Errorf("bridge: parse daemon_pubkey: %w", err)
		}
		r.bridgeKey = sk
		r.daemonPub = pub
		r.prologue = []byte(fmt.Sprintf("agbridge/v1|%s|%s", cfg.GatewayURL, cfg.TargetDaemon))
	}
	return r, nil
}

// currentConn returns the live wss.Conn under lock. Callers should treat the
// returned pointer as ephemeral — a concurrent replaceConn may swap it out
// immediately after.
func (r *router) currentConn() *wss.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

// replaceConn swaps in newConn and closes every pending call channel so the
// in-flight handler returns network_lost. Caller is the outer reconnect
// loop; replaceConn(nil) clears the E2E session as well (a fresh handshake
// is performed on the next connect, preserving forward secrecy).
func (r *router) replaceConn(newConn *wss.Conn) {
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
// Called from bridgeDialAndAttach after successful Noise handshake.
func (r *router) installSession(s *e2e.Session, sid [8]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.session = s
	r.sessionID = sid
}

// sendInner wraps inner as a Route frame and writes to the gateway.
// If an E2E session is active, the inner frame is AEAD-encrypted with
// AD = sessionID before HMAC signing. session_id is prepended to payload
// so the daemon can demultiplex its session map.
func (r *router) sendInner(ctx context.Context, inner proto.Frame) error {
	innerBytes, err := inner.Encode()
	if err != nil {
		return err
	}
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
func (r *router) decodeInbound(payload []byte) (proto.Frame, error) {
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

// doBridgeNoiseHandshake runs the Noise IK handshake over the freshly
// authenticated WSS connection, returning the session and its 8-byte ID.
// Called by bridgeDialAndAttach when e2e is enabled.
func doBridgeNoiseHandshake(ctx context.Context, conn *wss.Conn, rt *router) (*e2e.Session, [8]byte, error) {
	var sid [8]byte
	if _, err := rand.Read(sid[:]); err != nil {
		return nil, sid, fmt.Errorf("sid rand: %w", err)
	}
	init, err := e2e.NewInitiator(rt.bridgeKey, rt.daemonPub, rt.prologue)
	if err != nil {
		return nil, sid, err
	}
	msg1, err := init.WriteMessage1()
	if err != nil {
		return nil, sid, err
	}
	if err := conn.Send(ctx, proto.Frame{
		Type:    proto.FrameTypeNoiseInit,
		ReqID:   hex.EncodeToString(sid[:]),
		Payload: msg1,
	}); err != nil {
		return nil, sid, err
	}
	resp, err := conn.Recv(ctx)
	if err != nil {
		return nil, sid, err
	}
	if resp.Type == proto.FrameTypeError {
		return nil, sid, fmt.Errorf("daemon rejected handshake: %s", string(resp.Payload))
	}
	if resp.Type != proto.FrameTypeNoiseResp {
		return nil, sid, fmt.Errorf("expected NoiseResp, got type %d", resp.Type)
	}
	sess, err := init.ReadMessage2(resp.Payload)
	if err != nil {
		return nil, sid, err
	}
	return sess, sid, nil
}

// send writes f via the current conn. Returns errNoConn if there's no live
// connection (in a reconnect window).
func (r *router) send(ctx context.Context, f proto.Frame) error {
	conn := r.currentConn()
	if conn == nil {
		return errNoConn
	}
	return conn.Send(ctx, f)
}

// bridgeDialAndAttach dials the gateway, runs the bridge handshake (Hello
// then optional Noise IK), and hands the live conn to rt via replaceConn.
// On any failure it closes the new conn and returns the error.
func bridgeDialAndAttach(ctx context.Context, rt *router, cfg *config.BridgeConfig, tlsCfg *tls.Config) error {
	conn, err := wss.Dial(ctx, cfg.GatewayURL, transport.Credentials{}, tlsCfg)
	if err != nil {
		return err
	}
	hello := handshake.Hello{Role: "bridge", Name: cfg.AgentName, Secret: cfg.APIKey, TargetDaemon: cfg.TargetDaemon}
	helloPayload, _ := hello.Encode()
	if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: helloPayload}); err != nil {
		_ = conn.Close()
		return err
	}
	ack, err := conn.Recv(ctx)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if ack.Type != proto.FrameTypeHelloAck {
		_ = conn.Close()
		return fmt.Errorf("handshake nack: %v", ack.Type)
	}

	// Optional Noise IK handshake. Must happen BEFORE replaceConn so that
	// in-flight Route frames are never sent through a conn whose session is
	// half-initialized.
	var sess *e2e.Session
	var sid [8]byte
	if rt.e2eMode != "disabled" {
		sess, sid, err = doBridgeNoiseHandshake(ctx, conn, rt)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("noise handshake: %w", err)
		}
	}

	rt.replaceConn(conn)
	if sess != nil {
		rt.installSession(sess, sid)
	}
	return nil
}

// superviseBridgeConn runs rt.runReader to completion (blocks until the
// current conn dies), then loops via dialer.Loop to re-dial + re-handshake +
// re-attach. In-flight tool calls are signalled via replaceConn(nil) so they
// return network_lost(retryable=true). Exits when ctx cancels.
func superviseBridgeConn(ctx context.Context, rt *router, cfg *config.BridgeConfig, tlsCfg *tls.Config) {
	for {
		rt.runReader()
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "bridge: connection lost, reconnecting…")
		rt.replaceConn(nil)
		if err := dialer.Loop(ctx, func(c context.Context) error {
			return bridgeDialAndAttach(c, rt, cfg, tlsCfg)
		}, dialer.Options{}); err != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "bridge: reconnected")
	}
}

// runReader dispatches every inner frame by its ReqID until the conn dies
// or ctx cancels. Per-call frames (exec, read_file, write_file) and
// per-stream frames (port_forward StreamAck/Data/Close) share the same
// routing table since the daemon sets ReqID = streamID on stream frames.
// Caller is expected to call replaceConn before re-invoking runReader on a
// fresh conn (e.g., from the outer reconnect loop).
func (r *router) runReader() {
	conn := r.currentConn()
	if conn == nil {
		return
	}
	for {
		f, err := conn.Recv(r.ctx)
		if err != nil {
			return
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

func (r *router) registerCall(reqID string) <-chan proto.Frame {
	ch := make(chan proto.Frame, 64)
	r.mu.Lock()
	r.pending[reqID] = ch
	r.mu.Unlock()
	return ch
}

func (r *router) unregisterCall(reqID string) {
	r.mu.Lock()
	delete(r.pending, reqID)
	r.mu.Unlock()
}

// registerStream / unregisterStream are aliases for use by port_forward, to
// make caller intent explicit. Stream IDs share the ReqID keyspace.
func (r *router) registerStream(streamID string) <-chan proto.Frame {
	return r.registerCall(streamID)
}

func (r *router) unregisterStream(streamID string) { r.unregisterCall(streamID) }

type execArgs struct {
	Cmd       string            `json:"cmd"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	TimeoutMs int               `json:"timeout_ms"`
}

func (r *router) execHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args execArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	reqID := newReqID()
	req := execproto.ExecRequest{
		Cmd: args.Cmd, Args: args.Args, Cwd: args.Cwd, Env: args.Env, TimeoutMs: args.TimeoutMs,
	}
	reqJSON, err := req.Encode()
	if err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeExecRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	var stdout, stderr strings.Builder
	for {
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case f, ok := <-ch:
			if !ok {
				return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
			}
			switch f.Type {
			case proto.FrameTypeExecChunk:
				c, err := execproto.DecodeExecChunk(f.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				dst := &stdout
				if c.Stream == "stderr" {
					dst = &stderr
				}
				if dst.Len()+len(c.Data) > maxBufferedOutput {
					n := maxBufferedOutput - dst.Len()
					if n > 0 {
						dst.Write(c.Data[:n])
					}
				} else {
					dst.Write(c.Data)
				}
			case proto.FrameTypeExecComplete:
				c, err := execproto.DecodeExecComplete(f.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				return buildExecResult(stdout.String(), stderr.String(), c), nil
			case proto.FrameTypeError:
				return mcpErrorResult(errcode.New(string(f.Payload), "daemon error")), nil
			}
		}
	}
}

func buildExecResult(stdout, stderr string, c execproto.ExecComplete) any {
	text := fmt.Sprintf("exitcode=%d duration_ms=%d\n--- stdout ---\n%s--- stderr ---\n%s",
		c.ExitCode, c.DurationMs, stdout, stderr)
	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"_meta": map[string]any{
			"exitcode":    c.ExitCode,
			"duration_ms": c.DurationMs,
			"timed_out":   c.TimedOut,
			"truncated":   c.Truncated,
			"stdout_b64":  base64.StdEncoding.EncodeToString([]byte(stdout)),
			"stderr_b64":  base64.StdEncoding.EncodeToString([]byte(stderr)),
		},
	}
	if c.TimedOut {
		result["isError"] = true
		errMeta := errcode.New("exec_timeout", "subprocess exceeded timeout_ms").ToMCPMeta()
		for k, v := range metaToMap(errMeta) {
			result["_meta"].(map[string]any)[k] = v
		}
	}
	return result
}

type readFileArgs struct {
	Path    string `json:"path"`
	MaxSize int    `json:"max_size"`
}

func (r *router) readFileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args readFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	reqID := newReqID()
	reqJSON, _ := fileproto.FileReadRequest{Path: args.Path, MaxSize: args.MaxSize}.Encode()

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileReadRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	var content []byte
	for {
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case f, ok := <-ch:
			if !ok {
				return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
			}
			switch f.Type {
			case proto.FrameTypeFileChunk:
				c, err := fileproto.DecodeFileChunk(f.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				if len(content)+len(c.Data) > maxBufferedOutput {
					return mcpErrorResult(errcode.New("exceeds_max_size", "file exceeds 10 MB cap")), nil
				}
				content = append(content, c.Data...)
			case proto.FrameTypeFileComplete:
				c, _ := fileproto.DecodeFileComplete(f.Payload)
				return buildReadFileResult(content, c), nil
			case proto.FrameTypeError:
				return mcpErrorResult(errcode.New(string(f.Payload), "daemon error")), nil
			}
		}
	}
}

func buildReadFileResult(content []byte, c fileproto.FileComplete) any {
	if c.Err != "" {
		return mcpErrorResult(errcode.New(c.Err, "read_file: "+c.Err))
	}
	var text string
	if utf8.Valid(content) {
		text = string(content)
	} else {
		text = fmt.Sprintf("(binary %d bytes, sha256=%s)", c.Size, c.Sha256)
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"_meta": map[string]any{
			"size":        c.Size,
			"sha256":      c.Sha256,
			"content_b64": base64.StdEncoding.EncodeToString(content),
		},
	}
}

type writeFileArgs struct {
	Path       string `json:"path"`
	ContentB64 string `json:"content_b64"`
	Mode       uint32 `json:"mode"`
}

const writeChunkSize = 64 * 1024

func (r *router) writeFileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args writeFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	content, err := base64.StdEncoding.DecodeString(args.ContentB64)
	if err != nil {
		return mcpErrorResult(errcode.New("bad_payload", "content_b64: "+err.Error())), nil
	}
	if len(content) > maxBufferedOutput {
		return mcpErrorResult(errcode.New("exceeds_max_size", "content exceeds 10 MB cap")), nil
	}

	reqID := newReqID()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	reqJSON, _ := fileproto.FileWriteRequest{Path: args.Path, Mode: args.Mode}.Encode()
	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileWriteRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	if len(content) == 0 {
		if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Eof: true}); err != nil {
			return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
		}
	} else {
		for off := 0; off < len(content); off += writeChunkSize {
			end := off + writeChunkSize
			if end > len(content) {
				end = len(content)
			}
			chunk := fileproto.FileChunk{Data: content[off:end], Eof: end == len(content)}
			if err := r.sendFileChunk(ctx, reqID, chunk); err != nil {
				return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case f, ok := <-ch:
			if !ok {
				return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
			}
			switch f.Type {
			case proto.FrameTypeFileComplete:
				c, _ := fileproto.DecodeFileComplete(f.Payload)
				if c.Err != "" {
					return mcpErrorResult(errcode.New(c.Err, "write_file: "+c.Err)), nil
				}
				return map[string]any{
					"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("wrote %d bytes (sha256=%s)", c.Size, c.Sha256)}},
					"_meta": map[string]any{
						"bytes_written": c.Size,
						"sha256":        c.Sha256,
					},
				}, nil
			case proto.FrameTypeError:
				return mcpErrorResult(errcode.New(string(f.Payload), "daemon error")), nil
			}
		}
	}
}

func (r *router) sendFileChunk(ctx context.Context, reqID string, c fileproto.FileChunk) error {
	payload, _ := c.Encode()
	return r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileChunk, ReqID: reqID, Payload: payload})
}

type portForwardArgs struct {
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
}

const (
	streamAckTimeout      = 10 * time.Second
	streamReadBuffer      = 32 * 1024
	portForwardListenHost = "127.0.0.1"
)

func (r *router) portForwardHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args portForwardArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.RemoteHost == "" || args.RemotePort == 0 {
		return mcpErrorResult(errcode.New("bad_payload", "remote_host and remote_port required")), nil
	}
	addr := fmt.Sprintf("%s:%d", portForwardListenHost, args.LocalPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return mcpErrorResult(errcode.New("listen_failed", err.Error())), nil
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)
	go r.portForwardAcceptLoop(ctx, ln, args.RemoteHost, args.RemotePort)
	text := fmt.Sprintf("listening on %s:%d → %s:%d", portForwardListenHost, tcpAddr.Port, args.RemoteHost, args.RemotePort)
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"_meta": map[string]any{
			"local_host": portForwardListenHost,
			"local_port": tcpAddr.Port,
		},
	}, nil
}

func (r *router) portForwardAcceptLoop(ctx context.Context, ln net.Listener, host string, port int) {
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go r.portForwardStream(ctx, conn, host, port)
	}
}

func (r *router) portForwardStream(ctx context.Context, conn net.Conn, host string, port int) {
	defer conn.Close()
	streamID := newReqID()
	ch := r.registerStream(streamID)
	defer r.unregisterStream(streamID)

	openPayload, _ := streamproto.StreamOpen{StreamID: streamID, RemoteHost: host, RemotePort: port}.Encode()
	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeStreamOpen, ReqID: streamID, Payload: openPayload}); err != nil {
		return
	}

	ackCtx, ackCancel := context.WithTimeout(ctx, streamAckTimeout)
	defer ackCancel()
	for {
		select {
		case <-ackCtx.Done():
			return
		case f, ok := <-ch:
			if !ok {
				return
			}
			if f.Type == proto.FrameTypeStreamAck {
				ack, _ := streamproto.DecodeStreamAck(f.Payload)
				if !ack.Ok {
					return
				}
				goto connected
			}
		}
	}
connected:
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		defer cancel()
		buf := make([]byte, streamReadBuffer)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				sdPayload, _ := streamproto.StreamData{StreamID: streamID, Data: data}.Encode()
				if serr := r.sendInner(streamCtx, proto.Frame{Type: proto.FrameTypeStreamData, ReqID: streamID, Payload: sdPayload}); serr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		defer cancel()
		for {
			select {
			case <-streamCtx.Done():
				return
			case f, ok := <-ch:
				if !ok {
					return
				}
				switch f.Type {
				case proto.FrameTypeStreamData:
					sd, err := streamproto.DecodeStreamData(f.Payload)
					if err != nil {
						return
					}
					if _, werr := conn.Write(sd.Data); werr != nil {
						return
					}
				case proto.FrameTypeStreamClose:
					return
				}
			}
		}
	}()

	<-streamCtx.Done()

	closePayload, _ := streamproto.StreamClose{StreamID: streamID}.Encode()
	_ = r.sendInner(context.Background(), proto.Frame{Type: proto.FrameTypeStreamClose, ReqID: streamID, Payload: closePayload})
}

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

func newReqID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
