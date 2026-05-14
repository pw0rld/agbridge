package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRegisterAndList(t *testing.T) {
	srv := NewServer()
	srv.RegisterTool(ToolSpec{
		Name:        "echo",
		Description: "Echoes args back",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	_ = srv.Serve(context.Background(), in, &out)

	var resp map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp)
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "echo" {
		t.Errorf("name: %v", tool["name"])
	}
}

func TestCallToolDispatches(t *testing.T) {
	srv := NewServer()
	srv.RegisterTool(ToolSpec{
		Name:        "echo",
		Description: "Echoes",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "world"}}}, nil
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}` + "\n")
	var out bytes.Buffer
	_ = srv.Serve(context.Background(), in, &out)

	var resp map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", resp)
	}
	content := result["content"].([]any)
	if content[0].(map[string]any)["text"] != "world" {
		t.Errorf("content text: %+v", content)
	}
}

func TestCallUnknownToolReturnsError(t *testing.T) {
	srv := NewServer()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}` + "\n")
	var out bytes.Buffer
	_ = srv.Serve(context.Background(), in, &out)

	var resp map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp)
	if resp["error"] == nil {
		t.Errorf("expected error, got %+v", resp)
	}
}
