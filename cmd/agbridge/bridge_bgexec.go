package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pw0rld/agbridge/internal/bgproto"
	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/mcp"
	"github.com/pw0rld/agbridge/internal/proto"
)

type bgToolEntry struct {
	Spec    mcp.ToolSpec
	Handler mcp.ToolHandler
}

func bgExecTools(rt *router) []bgToolEntry {
	return []bgToolEntry{
		{
			Spec: mcp.ToolSpec{
				Name: "exec_start",
				Description: "Spawn a long-running background process. Returns immediately with " +
					"process_id. Use exec_logs to read its output, exec_status to poll, " +
					"exec_kill to terminate. Ideal for `npm run dev`, `cargo run`, test " +
					"watchers etc.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"cmd":   map[string]any{"type": "string"},
						"args":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"cwd":   map[string]any{"type": "string"},
						"env":   map[string]any{"type": "object"},
						"label": map[string]any{"type": "string", "description": "Optional human-readable name"},
					},
					"required": []string{"cmd", "cwd"},
				},
			},
			Handler: makeBgHandler(rt, "start"),
		},
		{
			Spec: mcp.ToolSpec{
				Name: "exec_status",
				Description: "Poll a background process. Returns state (running|exited|killed|failed) " +
					"plus exitcode, started_at, ended_at, last_output_at, duration_ms.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"process_id": map[string]any{"type": "string"},
					},
					"required": []string{"process_id"},
				},
			},
			Handler: makeBgHandler(rt, "status"),
		},
		{
			Spec: mcp.ToolSpec{
				Name: "exec_logs",
				Description: "Read stdout+stderr of a background process. Per-stream ring buffer " +
					"is 1 MiB; older bytes drop with stdout_truncated/stderr_truncated set. " +
					"Either pass tail_lines=N to get the last N lines, OR since_offset=X " +
					"to get everything since byte X (use next_offset from prior call for " +
					"polling).",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"process_id":   map[string]any{"type": "string"},
						"tail_lines":   map[string]any{"type": "integer", "minimum": 0},
						"since_offset": map[string]any{"type": "integer", "minimum": 0},
					},
					"required": []string{"process_id"},
				},
			},
			Handler: makeBgHandler(rt, "logs"),
		},
		{
			Spec: mcp.ToolSpec{
				Name: "exec_kill",
				Description: "Signal a background process. Defaults to TERM (graceful); KILL " +
					"forces immediate; INT for Ctrl-C semantics. The signal is sent to the " +
					"process group so child processes (e.g. webpack-dev-server spawned by " +
					"`npm run dev`) also die.",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"process_id": map[string]any{"type": "string"},
						"signal":     map[string]any{"type": "string", "enum": []string{"TERM", "KILL", "INT"}},
					},
					"required": []string{"process_id"},
				},
			},
			Handler: makeBgHandler(rt, "kill"),
		},
		{
			Spec: mcp.ToolSpec{
				Name: "exec_list",
				Description: "List all background processes tracked by this daemon (running and " +
					"recently exited). Finished processes are garbage-collected after 1 hour.",
				InputSchema: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			Handler: makeBgHandler(rt, "list"),
		},
	}
}

type bgArgs struct {
	Cmd         string            `json:"cmd"`
	Args        []string          `json:"args"`
	Cwd         string            `json:"cwd"`
	Env         map[string]string `json:"env"`
	Label       string            `json:"label"`
	ProcessID   string            `json:"process_id"`
	TailLines   int               `json:"tail_lines"`
	SinceOffset int64             `json:"since_offset"`
	Signal      string            `json:"signal"`
}

func makeBgHandler(rt *router, action string) mcp.ToolHandler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var a bgArgs
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &a); err != nil {
				return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
			}
		}
		req := bgproto.BgExecRequest{
			Action:      action,
			Cmd:         a.Cmd,
			Args:        a.Args,
			Cwd:         a.Cwd,
			Env:         a.Env,
			Label:       a.Label,
			ProcessID:   a.ProcessID,
			TailLines:   a.TailLines,
			SinceOffset: a.SinceOffset,
			Signal:      a.Signal,
		}
		reqJSON, _ := req.Encode()
		reqID := newReqID()
		ch := rt.registerCall(reqID)
		defer rt.unregisterCall(reqID)

		if err := rt.sendInner(ctx, proto.Frame{Type: proto.FrameTypeBgExecRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
			return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
		}
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case f, ok := <-ch:
			if !ok {
				return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
			}
			switch f.Type {
			case proto.FrameTypeBgExecResponse:
				resp, _ := bgproto.DecodeBgExecResponse(f.Payload)
				if resp.Err != "" {
					msg := "exec_" + action + ": " + resp.Err
					if resp.ErrDetail != "" {
						msg = msg + " (" + resp.ErrDetail + ")"
					}
					return mcpErrorResult(errcode.New(resp.Err, msg)), nil
				}
				return formatBgResponse(action, resp), nil
			case proto.FrameTypeError:
				code, detail := errcode.DecodeFramePayload(f.Payload)
				msg := "exec_" + action + ": " + code
				if detail != "" {
					msg = msg + " (" + detail + ")"
				}
				return mcpErrorResult(errcode.New(code, msg)), nil
			}
			return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame %d", f.Type))), nil
		}
	}
}

func formatBgResponse(action string, resp bgproto.BgExecResponse) map[string]any {
	var text string
	meta := map[string]any{}
	switch action {
	case "start":
		text = fmt.Sprintf("started process_id=%s label=%q", resp.ProcessID, resp.Label)
		meta["process_id"] = resp.ProcessID
		meta["label"] = resp.Label
		meta["state"] = resp.State
		meta["started_at"] = resp.StartedAt
	case "status", "kill":
		text = fmt.Sprintf("state=%s exit=%d duration_ms=%d", resp.State, resp.Exitcode, resp.DurationMs)
		meta["process_id"] = resp.ProcessID
		meta["state"] = resp.State
		meta["exitcode"] = resp.Exitcode
		meta["duration_ms"] = resp.DurationMs
		meta["started_at"] = resp.StartedAt
		meta["ended_at"] = resp.EndedAt
		meta["last_output_at"] = resp.LastOutputAt
	case "logs":
		text = resp.Stdout
		if resp.Stderr != "" {
			text += "\n--- stderr ---\n" + resp.Stderr
		}
		meta["process_id"] = resp.ProcessID
		meta["stdout"] = resp.Stdout
		meta["stderr"] = resp.Stderr
		meta["stdout_truncated"] = resp.StdoutTrunc
		meta["stderr_truncated"] = resp.StderrTrunc
		meta["next_offset"] = resp.NextOffset
	case "list":
		var b []byte
		for _, p := range resp.Processes {
			b = append(b, []byte(fmt.Sprintf("  %s  state=%s  exit=%d  cmd=%s  label=%q\n",
				p.ProcessID, p.State, p.Exitcode, p.Cmd, p.Label))...)
		}
		if len(b) == 0 {
			text = "(no background processes)"
		} else {
			text = string(b)
		}
		meta["processes"] = resp.Processes
		meta["count"] = len(resp.Processes)
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"_meta":   meta,
	}
}
