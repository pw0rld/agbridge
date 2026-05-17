// Package bgproto defines the request/response schema for background
// process management. One Frame type (BgExecRequest/Response) carries
// all five actions: start, status, logs, kill, list.
package bgproto

import "encoding/json"

// BgExecRequest is the union shape for all five background-exec tools.
type BgExecRequest struct {
	Action string `json:"action"` // "start" | "status" | "logs" | "kill" | "list"

	// Start fields
	Cmd       string            `json:"cmd,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Label     string            `json:"label,omitempty"`

	// Process-targeted fields (status / logs / kill)
	ProcessID string `json:"process_id,omitempty"`

	// Logs fields
	TailLines   int   `json:"tail_lines,omitempty"`
	SinceOffset int64 `json:"since_offset,omitempty"`

	// Kill fields
	Signal string `json:"signal,omitempty"` // "TERM" (default) | "KILL" | "INT"
}

// BgExecResponse is also a union — fields set depending on Action.
type BgExecResponse struct {
	Action    string `json:"action"`
	Err       string `json:"err,omitempty"`
	ErrDetail string `json:"err_detail,omitempty"`

	// Start
	ProcessID string `json:"process_id,omitempty"`
	Label     string `json:"label,omitempty"`

	// Status (also returned by Kill on success)
	State        string `json:"state,omitempty"` // "running" | "exited" | "killed" | "failed"
	Exitcode     int    `json:"exitcode"`
	StartedAt    int64  `json:"started_at,omitempty"`
	EndedAt      int64  `json:"ended_at,omitempty"`
	LastOutputAt int64  `json:"last_output_at,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`

	// Logs
	Stdout      string `json:"stdout,omitempty"`
	Stderr      string `json:"stderr,omitempty"`
	StdoutTrunc bool   `json:"stdout_truncated,omitempty"`
	StderrTrunc bool   `json:"stderr_truncated,omitempty"`
	NextOffset  int64  `json:"next_offset,omitempty"`

	// List
	Processes []BgProcessInfo `json:"processes,omitempty"`
}

// BgProcessInfo is the per-process summary returned by Action="list".
type BgProcessInfo struct {
	ProcessID string `json:"process_id"`
	Label     string `json:"label,omitempty"`
	Cmd       string `json:"cmd"`
	State     string `json:"state"`
	Exitcode  int    `json:"exitcode"`
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
}

func (r BgExecRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
func (r BgExecResponse) Encode() ([]byte, error) { return json.Marshal(r) }

func DecodeBgExecRequest(b []byte) (BgExecRequest, error) {
	var r BgExecRequest
	err := json.Unmarshal(b, &r)
	return r, err
}

func DecodeBgExecResponse(b []byte) (BgExecResponse, error) {
	var r BgExecResponse
	err := json.Unmarshal(b, &r)
	return r, err
}
