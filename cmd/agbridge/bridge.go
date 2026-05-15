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

			rt := newRouter(ctx, nil, []byte(cfg.APIKey))

			// First connect synchronously so we never serve MCP before an
			// initial handshake succeeds — otherwise the very first tool call
			// would observe network_lost.
			if err := bridgeDialAndAttach(ctx, rt, cfg, tlsCfg); err != nil {
				return fmt.Errorf("initial handshake: %w", err)
			}
			fmt.Fprintln(os.Stderr, "bridge: handshake ok, starting MCP stdio server")

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
	ctx     context.Context
	apiKey  []byte
	mu      sync.Mutex
	conn    *wss.Conn
	pending map[string]chan proto.Frame
}

func newRouter(ctx context.Context, conn *wss.Conn, apiKey []byte) *router {
	return &router{
		ctx:     ctx,
		conn:    conn,
		apiKey:  apiKey,
		pending: make(map[string]chan proto.Frame),
	}
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
// loop; replaceConn(nil) is a clean shutdown signal.
func (r *router) replaceConn(newConn *wss.Conn) {
	r.mu.Lock()
	old := r.pending
	r.pending = make(map[string]chan proto.Frame)
	r.conn = newConn
	r.mu.Unlock()
	for _, ch := range old {
		close(ch)
	}
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

// bridgeDialAndAttach dials the gateway, runs the bridge handshake, and
// hands the live conn to rt via replaceConn. On any handshake failure it
// closes the new conn and returns the error.
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
	rt.replaceConn(conn)
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
		inner, err := proto.Decode(f.Payload)
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
	inner, err := proto.Frame{Type: proto.FrameTypeExecRequest, ReqID: reqID, Payload: reqJSON}.Encode()
	if err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	signed := auth.SignFrame(r.apiKey, inner)

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed}); err != nil {
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
	inner, _ := proto.Frame{Type: proto.FrameTypeFileReadRequest, ReqID: reqID, Payload: reqJSON}.Encode()
	signed := auth.SignFrame(r.apiKey, inner)

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed}); err != nil {
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
	inner, _ := proto.Frame{Type: proto.FrameTypeFileWriteRequest, ReqID: reqID, Payload: reqJSON}.Encode()
	signed := auth.SignFrame(r.apiKey, inner)
	if err := r.send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed}); err != nil {
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
	inner, _ := proto.Frame{Type: proto.FrameTypeFileChunk, ReqID: reqID, Payload: payload}.Encode()
	signed := auth.SignFrame(r.apiKey, inner)
	return r.send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed})
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
	openInner, _ := proto.Frame{Type: proto.FrameTypeStreamOpen, ReqID: streamID, Payload: openPayload}.Encode()
	if err := r.send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: auth.SignFrame(r.apiKey, openInner)}); err != nil {
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
				sdInner, _ := proto.Frame{Type: proto.FrameTypeStreamData, ReqID: streamID, Payload: sdPayload}.Encode()
				if serr := r.send(streamCtx, proto.Frame{Type: proto.FrameTypeRoute, Payload: auth.SignFrame(r.apiKey, sdInner)}); serr != nil {
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
	closeInner, _ := proto.Frame{Type: proto.FrameTypeStreamClose, ReqID: streamID, Payload: closePayload}.Encode()
	_ = r.send(context.Background(), proto.Frame{Type: proto.FrameTypeRoute, Payload: auth.SignFrame(r.apiKey, closeInner)})
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
