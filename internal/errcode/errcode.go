// Package errcode defines structured error codes carried across the
// bridge ↔ daemon path and surfaced to MCP agents via the _meta field.
package errcode

// Error is a structured failure record.
type Error struct {
	Code    string
	Message string
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
