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

func TestFramePayloadRoundTrip(t *testing.T) {
	b := EncodeFramePayload("path_forbidden", "allowed: [/foo, /bar]")
	code, detail := DecodeFramePayload(b)
	if code != "path_forbidden" {
		t.Errorf("code: %q", code)
	}
	if detail != "allowed: [/foo, /bar]" {
		t.Errorf("detail: %q", detail)
	}
}

func TestFramePayloadDecodeRawString(t *testing.T) {
	// Backward compat: old daemons send raw code strings.
	code, detail := DecodeFramePayload([]byte("exec_failed"))
	if code != "exec_failed" {
		t.Errorf("code: %q", code)
	}
	if detail != "" {
		t.Errorf("detail should be empty for raw payload: %q", detail)
	}
}

func TestFramePayloadDecodeEmpty(t *testing.T) {
	code, _ := DecodeFramePayload(nil)
	if code != "unknown" {
		t.Errorf("empty payload should default to 'unknown', got %q", code)
	}
}

func TestFramePayloadDecodeMalformedJSON(t *testing.T) {
	// JSON-looking but malformed: must NOT crash, must treat as raw.
	code, detail := DecodeFramePayload([]byte("{garbage"))
	if code != "{garbage" {
		t.Errorf("code: %q", code)
	}
	if detail != "" {
		t.Errorf("detail: %q", detail)
	}
}
