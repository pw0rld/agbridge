// Package execproto defines the JSON payload schemas for ExecRequest,
// ExecChunk, ExecComplete frames. Binary chunk data is base64-encoded
// on the wire (encoding/json's []byte handling).
package execproto

import "encoding/json"

// ExecRequest is the bridge → daemon command-run request.
type ExecRequest struct {
	Cmd       string            `json:"cmd"`
	Args      []string          `json:"args,omitempty"`
	Cwd       string            `json:"cwd"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMs int               `json:"timeout_ms,omitempty"`
}

// ExecChunk is a slice of stdout or stderr streaming from daemon → bridge.
// Data is base64-encoded on the wire (Go's encoding/json default for []byte).
type ExecChunk struct {
	Stream string `json:"stream"` // "stdout" | "stderr"
	Data   []byte `json:"data"`
}

// ExecComplete signals subprocess termination. Bridge SHOULD treat receiving
// this as the end-of-stream marker for that request.
type ExecComplete struct {
	ExitCode   int  `json:"exitcode"`
	DurationMs int  `json:"duration_ms"`
	TimedOut   bool `json:"timed_out,omitempty"`
	Truncated  bool `json:"truncated,omitempty"`
}

func (r ExecRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
func (c ExecChunk) Encode() ([]byte, error)    { return json.Marshal(c) }
func (c ExecComplete) Encode() ([]byte, error) { return json.Marshal(c) }

func DecodeExecRequest(b []byte) (ExecRequest, error) {
	var r ExecRequest
	err := json.Unmarshal(b, &r)
	return r, err
}

func DecodeExecChunk(b []byte) (ExecChunk, error) {
	var c ExecChunk
	err := json.Unmarshal(b, &c)
	return c, err
}

func DecodeExecComplete(b []byte) (ExecComplete, error) {
	var c ExecComplete
	err := json.Unmarshal(b, &c)
	return c, err
}
