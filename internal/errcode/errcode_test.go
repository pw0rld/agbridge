package errcode

import (
	"encoding/json"
	"testing"
)

func TestNewToMCP(t *testing.T) {
	e := New("path_forbidden", "path /etc/passwd not in allowed_paths")
	b, err := json.Marshal(e.ToMCPMeta())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["error_code"] != "path_forbidden" {
		t.Errorf("error_code: %v", got["error_code"])
	}
	if got["category"] != "authz" {
		t.Errorf("category: %v", got["category"])
	}
	if got["retryable"] != false {
		t.Errorf("retryable: %v", got["retryable"])
	}
}

func TestNetworkLostIsRetryable(t *testing.T) {
	e := New("network_lost", "WSS dropped")
	if !e.ToMCPMeta().Retryable {
		t.Errorf("network_lost should be retryable")
	}
	if e.ToMCPMeta().Category != "network" {
		t.Errorf("category: %s", e.ToMCPMeta().Category)
	}
}

func TestUnknownCodeDefaults(t *testing.T) {
	e := New("totally_made_up", "msg")
	m := e.ToMCPMeta()
	if m.Category != "unknown" || m.Retryable {
		t.Errorf("got %+v", m)
	}
}
