package mcp

import (
	"context"
	"encoding/json"
)

// ToolSpec is the MCP-facing description of a tool.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolHandler executes a tool given JSON arguments and returns the MCP
// "result" object (typically of the shape {"content": [{"type":"text",...}]}
// or {"isError": true, ...}).
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

// ProgressEmitter writes one MCP `notifications/progress` event. Total is
// optional (use nil when the work has no known upper bound — e.g. exec
// output streaming). Message is shown to the user; keep it short.
type ProgressEmitter func(progress float64, total *float64, message string)

type progressEmitterKey struct{}

// EmitterFromContext returns the progress emitter installed by the server
// for the current tools/call. When the caller didn't include a
// progressToken in `_meta`, returns a no-op emitter — handlers can call
// it unconditionally.
func EmitterFromContext(ctx context.Context) ProgressEmitter {
	if e, ok := ctx.Value(progressEmitterKey{}).(ProgressEmitter); ok && e != nil {
		return e
	}
	return func(_ float64, _ *float64, _ string) {}
}

func withEmitter(ctx context.Context, e ProgressEmitter) context.Context {
	return context.WithValue(ctx, progressEmitterKey{}, e)
}

// RegisterTool adds a tool. Concurrent calls are safe.
func (s *Server) RegisterTool(spec ToolSpec, h ToolHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[spec.Name] = h
	s.specs[spec.Name] = spec
}

func (s *Server) listTools() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	tools := make([]ToolSpec, 0, len(s.specs))
	for _, sp := range s.specs {
		tools = append(tools, sp)
	}
	return map[string]any{"tools": tools}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      callParamsMeta  `json:"_meta,omitempty"`
}

type callParamsMeta struct {
	// ProgressToken per MCP spec — string OR number. We keep it as raw so
	// we round-trip whatever the client sent.
	ProgressToken json.RawMessage `json:"progressToken,omitempty"`
}

func (s *Server) callTool(ctx context.Context, req jsonRPCMessage) jsonRPCMessage {
	var p callParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errorResp(req.ID, codeInvalidRequest, "bad tools/call params: "+err.Error())
	}
	s.mu.Lock()
	h, ok := s.tools[p.Name]
	s.mu.Unlock()
	if !ok {
		return errorResp(req.ID, codeMethodNotFound, "unknown tool: "+p.Name)
	}
	emitter := s.makeProgressEmitter(p.Meta.ProgressToken)
	result, err := h(withEmitter(ctx, emitter), p.Arguments)
	if err != nil {
		return errorResp(req.ID, codeInternal, err.Error())
	}
	return okResp(req.ID, result)
}

// makeProgressEmitter returns either a no-op (when the client didn't send
// a progressToken) or a closure that wraps notify() with the token bound.
func (s *Server) makeProgressEmitter(token json.RawMessage) ProgressEmitter {
	if len(token) == 0 {
		return func(_ float64, _ *float64, _ string) {}
	}
	return func(progress float64, total *float64, message string) {
		params := map[string]any{
			"progressToken": token,
			"progress":      progress,
		}
		if total != nil {
			params["total"] = *total
		}
		if message != "" {
			params["message"] = message
		}
		s.notify("notifications/progress", params)
	}
}
