// Package mcp is a minimal MCP-flavored JSON-RPC 2.0 server over an arbitrary
// transport (typically stdin/stdout). It supports three built-in methods:
// "initialize", "tools/list", and "tools/call". Tool implementations are
// registered via Server.RegisterTool from tools.go.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// ProtocolVersion echoed in initialize responses. Bumped as MCP evolves.
const ProtocolVersion = "2024-11-05"

// jsonRPCMessage is the shape of both requests and responses on the wire.
type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternal       = -32603
)

// Server is a single-client (stdio) JSON-RPC server. NOT safe for concurrent
// use; one Server per MCP session.
type Server struct {
	mu    sync.Mutex
	tools map[string]ToolHandler
	specs map[string]ToolSpec
}

// NewServer constructs a fresh Server with no tools registered.
func NewServer() *Server {
	return &Server{
		tools: make(map[string]ToolHandler),
		specs: make(map[string]ToolSpec),
	}
}

// Serve reads JSON-RPC messages line-delimited from r and writes responses
// to w. Blocks until r returns EOF or ctx is cancelled.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := scanner.Bytes()
		var req jsonRPCMessage
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(errorResp(nil, codeParse, "parse error"))
			continue
		}
		if req.JSONRPC != "2.0" {
			_ = enc.Encode(errorResp(req.ID, codeInvalidRequest, "jsonrpc must be 2.0"))
			continue
		}
		resp := s.dispatch(ctx, req)
		if req.ID == nil {
			// notification — no response
			continue
		}
		_ = enc.Encode(resp)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (s *Server) dispatch(ctx context.Context, req jsonRPCMessage) jsonRPCMessage {
	switch req.Method {
	case "initialize":
		return okResp(req.ID, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{"name": "agbridge", "version": "0.0.3"},
		})
	case "tools/list":
		return okResp(req.ID, s.listTools())
	case "tools/call":
		return s.callTool(ctx, req)
	default:
		return errorResp(req.ID, codeMethodNotFound, "unknown method: "+req.Method)
	}
}

func okResp(id json.RawMessage, result any) jsonRPCMessage {
	return jsonRPCMessage{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResp(id json.RawMessage, code int, msg string) jsonRPCMessage {
	return jsonRPCMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// Placeholder declarations replaced in Task 8.
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (s *Server) listTools() any {
	return map[string]any{"tools": []any{}}
}

func (s *Server) callTool(_ context.Context, req jsonRPCMessage) jsonRPCMessage {
	return errorResp(req.ID, codeMethodNotFound, "no tools registered yet")
}
