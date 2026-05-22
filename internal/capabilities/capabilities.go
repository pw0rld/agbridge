// Package capabilities defines the JSON payload schema for the
// FrameTypeCapabilitiesResponse, used by the capabilities MCP tool so an
// AI agent can introspect its sandbox without trial-and-error.
package capabilities

import "encoding/json"

// Capabilities is the daemon-side snapshot returned to the bridge.
type Capabilities struct {
	DaemonName        string   `json:"daemon_name"`
	DaemonHostname    string   `json:"daemon_hostname,omitempty"`
	AllowedExecCwds   []string `json:"allowed_exec_cwds"`
	AllowedReadPaths  []string `json:"allowed_read_paths"`
	AllowedWritePaths []string `json:"allowed_write_paths"`
	EnvAllowlist      []string `json:"env_allowlist,omitempty"`
	ForbiddenPorts    []int    `json:"forbidden_ports,omitempty"`
	MaxFileBytes      int64    `json:"max_file_bytes"`
	AgbridgeVersion   string   `json:"agbridge_version,omitempty"`
}

func (c Capabilities) Encode() ([]byte, error) { return json.Marshal(c) }

func Decode(b []byte) (Capabilities, error) {
	var c Capabilities
	err := json.Unmarshal(b, &c)
	return c, err
}
