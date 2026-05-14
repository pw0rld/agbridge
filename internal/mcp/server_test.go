package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestInitializeReturnsProtocolVersion(t *testing.T) {
	srv := NewServer()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}` + "\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal: %v: %q", err, out.String())
	}
	if resp["id"].(float64) != 1 {
		t.Errorf("id: %v", resp["id"])
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %+v", resp)
	}
	if res["protocolVersion"] == nil {
		t.Errorf("missing protocolVersion in result: %+v", res)
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	srv := NewServer()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"no/such","params":{}}` + "\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp map[string]any
	_ = json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error: %+v", resp)
	}
	if errObj["code"].(float64) != -32601 {
		t.Errorf("code: %v", errObj["code"])
	}
}
