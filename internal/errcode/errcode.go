// Package errcode defines structured error codes carried across the
// bridge ↔ daemon path and surfaced to MCP agents via the _meta field.
package errcode

import "encoding/json"

// Error is a structured failure record.
type Error struct {
	Code    string
	Message string
}

// FramePayload is the wire shape for FrameTypeError payloads. New daemons
// emit JSON; old daemons emit raw code strings — decoders handle both.
type FramePayload struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// EncodeFramePayload serializes code + detail as JSON. The result is safe
// to put on a proto.Frame{Type: FrameTypeError}.
func EncodeFramePayload(code, detail string) []byte {
	b, _ := json.Marshal(FramePayload{Code: code, Detail: detail})
	return b
}

// DecodeFramePayload accepts either JSON ({"code":...,"detail":...}) or a
// raw code string and returns (code, detail). Empty input returns
// ("unknown", ""). This is the bridge-side counterpart to
// EncodeFramePayload and keeps wire compatibility with pre-T5 daemons.
func DecodeFramePayload(b []byte) (code, detail string) {
	if len(b) == 0 {
		return "unknown", ""
	}
	if b[0] == '{' {
		var p FramePayload
		if err := json.Unmarshal(b, &p); err == nil && p.Code != "" {
			return p.Code, p.Detail
		}
	}
	return string(b), ""
}

// Meta is the JSON-serializable shape attached to MCP tool responses
// under the optional "_meta" field. See spec §错误处理要点.
type Meta struct {
	ErrorCode    string `json:"error_code"`
	Category     string `json:"category"`
	Retryable    bool   `json:"retryable"`
	RetryAfterMs int    `json:"retry_after_ms,omitempty"`
	Message      string `json:"message,omitempty"`
}

// New constructs an Error with the given code and human message.
func New(code, message string) Error {
	return Error{Code: code, Message: message}
}

// ToMCPMeta classifies the error code into category + retryable and
// produces the wire-shape Meta.
func (e Error) ToMCPMeta() Meta {
	cat, retry := classify(e.Code)
	m := Meta{
		ErrorCode: e.Code,
		Category:  cat,
		Retryable: retry,
		Message:   e.Message,
	}
	if retry {
		m.RetryAfterMs = 1000
	}
	return m
}

func classify(code string) (category string, retryable bool) {
	switch code {
	case "auth_failed", "bad_mac":
		return "auth", false
	case "tool_forbidden", "path_forbidden", "port_forbidden", "daemon_not_allowed":
		return "authz", false
	case "network_lost", "daemon_offline", "daemon_send_failed", "daemon_recv_failed":
		return "network", true
	case "exec_timeout":
		return "exec", false
	case "exec_failed", "spawn_failed":
		return "exec", false
	case "protocol_error", "bad_payload":
		return "protocol", false
	default:
		return "unknown", false
	}
}
