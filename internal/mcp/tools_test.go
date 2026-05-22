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

func TestProgressEmitterStreamsNotifications(t *testing.T) {
	srv := NewServer()
	srv.RegisterTool(ToolSpec{
		Name:        "streamy",
		Description: "Emits progress",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		emit := EmitterFromContext(ctx)
		emit(10, nil, "ten")
		emit(20, nil, "twenty")
		total := 100.0
		emit(50, &total, "halfway")
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}, nil
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"streamy","arguments":{},"_meta":{"progressToken":"pt-1"}}}` + "\n")
	var out bytes.Buffer
	_ = srv.Serve(context.Background(), in, &out)

	// Out should contain 3 notifications then 1 result, in order, each on its own line.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (3 progress + 1 result), got %d:\n%s", len(lines), out.String())
	}
	// First three: progress notifications
	for i, expected := range []struct {
		progress float64
		message  string
		hasTotal bool
	}{
		{10, "ten", false},
		{20, "twenty", false},
		{50, "halfway", true},
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if m["method"] != "notifications/progress" {
			t.Errorf("line %d method: %v", i, m["method"])
		}
		params := m["params"].(map[string]any)
		if params["progress"].(float64) != expected.progress {
			t.Errorf("line %d progress: %v want %v", i, params["progress"], expected.progress)
		}
		if params["message"] != expected.message {
			t.Errorf("line %d message: %v want %v", i, params["message"], expected.message)
		}
		_, hasTotal := params["total"]
		if hasTotal != expected.hasTotal {
			t.Errorf("line %d total presence: got %v want %v", i, hasTotal, expected.hasTotal)
		}
	}
	// Last: result
	var resultMsg map[string]any
	_ = json.Unmarshal([]byte(lines[3]), &resultMsg)
	if _, ok := resultMsg["result"]; !ok {
		t.Errorf("last line should be the result message: %v", resultMsg)
	}
}

func TestNoProgressTokenMakesNoOpEmitter(t *testing.T) {
	srv := NewServer()
	srv.RegisterTool(ToolSpec{
		Name:        "no-token",
		Description: "tries to emit but no token",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, args json.RawMessage) (any, error) {
		emit := EmitterFromContext(ctx)
		emit(1, nil, "should be silent")
		emit(2, nil, "still silent")
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil
	})

	in := strings.NewReader(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"no-token","arguments":{}}}` + "\n")
	var out bytes.Buffer
	_ = srv.Serve(context.Background(), in, &out)

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected just the result, got %d lines:\n%s", len(lines), out.String())
	}
}
