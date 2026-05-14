package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/mcp"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

const maxBufferedOutput = 1 << 20

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
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			conn, err := wss.Dial(ctx, cfg.GatewayURL, transport.Credentials{}, tlsCfg)
			if err != nil {
				return err
			}
			defer conn.Close()

			hello := handshake.Hello{Role: "bridge", Name: cfg.AgentName, Secret: cfg.APIKey, TargetDaemon: cfg.TargetDaemon}
			helloPayload, _ := hello.Encode()
			if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: helloPayload}); err != nil {
				return err
			}
			ack, err := conn.Recv(ctx)
			if err != nil || ack.Type != proto.FrameTypeHelloAck {
				return fmt.Errorf("handshake failed: %v", ack.Type)
			}
			fmt.Fprintln(os.Stderr, "bridge: handshake ok, starting MCP stdio server")

			rt := newRouter(ctx, conn, []byte(cfg.APIKey))
			go rt.runReader()

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

			return srv.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to bridge YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

type router struct {
	ctx     context.Context
	conn    *wss.Conn
	apiKey  []byte
	mu      sync.Mutex
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

func (r *router) runReader() {
	for {
		f, err := r.conn.Recv(r.ctx)
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

	if err := r.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	var stdout, stderr strings.Builder
	for {
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case f := <-ch:
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
