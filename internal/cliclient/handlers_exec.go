package cliclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/mcp"
	"github.com/pw0rld/agbridge/internal/proto"
)

type execArgs struct {
	Cmd       string            `json:"cmd"`
	Args      []string          `json:"args"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env"`
	TimeoutMs int               `json:"timeout_ms"`
}

// ExecHandler runs a remote command on the daemon and returns the MCP
// tool result. Mirrors (*router).execHandler in cmd/agbridge/bridge.go.
func (r *Router) ExecHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args execArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	emit := mcp.EmitterFromContext(ctx)
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
				if int64(dst.Len())+int64(len(c.Data)) > maxBufferedOutput {
					n := maxBufferedOutput - int64(dst.Len())
					if n > 0 {
						dst.Write(c.Data[:int(n)])
					}
				} else {
					dst.Write(c.Data)
				}
				// Forward this chunk to the MCP client as a progress
				// notification. When the caller didn't request progress,
				// this is a no-op.
				emit(float64(stdout.Len()+stderr.Len()), nil, string(c.Data))
			case proto.FrameTypeExecComplete:
				c, err := execproto.DecodeExecComplete(f.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				return buildExecResult(stdout.String(), stderr.String(), c), nil
			case proto.FrameTypeError:
				code, detail := errcode.DecodeFramePayload(f.Payload)
				msg := "exec: " + code
				if detail != "" {
					msg = msg + " (" + detail + ")"
				}
				return mcpErrorResult(errcode.New(code, msg)), nil
			}
		}
	}
}

func buildExecResult(stdout, stderr string, c execproto.ExecComplete) any {
	meta := map[string]any{
		"exitcode":    c.ExitCode,
		"duration_ms": c.DurationMs,
		"timed_out":   c.TimedOut,
		"truncated":   c.Truncated,
	}
	stdoutUTF8 := utf8.ValidString(stdout)
	stderrUTF8 := utf8.ValidString(stderr)
	// F9: emit text OR b64, never both. Text saves ~33% tokens; b64
	// only when the bytes can't safely live in a JSON string.
	if stdoutUTF8 {
		meta["stdout"] = stdout
	} else {
		meta["stdout_b64"] = base64.StdEncoding.EncodeToString([]byte(stdout))
	}
	if stderrUTF8 {
		meta["stderr"] = stderr
	} else {
		meta["stderr_b64"] = base64.StdEncoding.EncodeToString([]byte(stderr))
	}

	// content[0].text: prioritize stdout (the usual interest) and only fall
	// through to stderr when stdout is empty. Non-zero exitcode prepends a
	// short status line so AI agents notice failures without parsing _meta.
	var text string
	body := stdout
	if !stdoutUTF8 {
		body = fmt.Sprintf("(stdout: %d binary bytes — see _meta.stdout_b64)", len(stdout))
	}
	if body == "" && stderr != "" {
		body = stderr
		if !stderrUTF8 {
			body = fmt.Sprintf("(stderr: %d binary bytes — see _meta.stderr_b64)", len(stderr))
		}
	}
	if c.ExitCode != 0 {
		text = fmt.Sprintf("exitcode=%d duration_ms=%d\n%s", c.ExitCode, c.DurationMs, body)
		if stderr != "" && stderrUTF8 && stdout != "" {
			text += "\n--- stderr ---\n" + stderr
		}
	} else {
		text = body
	}

	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"_meta":   meta,
	}
	if c.TimedOut {
		result["isError"] = true
		errMeta := errcode.New("exec_timeout", "subprocess exceeded timeout_ms").ToMCPMeta()
		for k, v := range metaToMap(errMeta) {
			meta[k] = v
		}
	}
	return result
}
