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
	result, err := h(ctx, p.Arguments)
	if err != nil {
		return errorResp(req.ID, codeInternal, err.Error())
	}
	return okResp(req.ID, result)
}
