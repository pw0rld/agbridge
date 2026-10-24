// Package capabilities defines the JSON payload schema for the
// FrameTypeCapabilitiesResponse, used by the capabilities MCP tool so an
// AI agent can introspect its sandbox without trial-and-error.
package capabilities

import "encoding/json"

// Capabilities is the daemon-side snapshot returned to the bridge.
type Capabilities struct {
	DaemonName        string   `json:"daemon_name"`
	DaemonHostname    string   `json:"daemon_hostname,omitempty"`
	ExecEnabled       bool     `json:"exec_enabled"`
	EnvAllowlist      []string `json:"env_allowlist,omitempty"`
	ForbiddenPorts    []int    `json:"forbidden_ports,omitempty"`
	MaxFileBytes      int64    `json:"max_file_bytes"`
	AgbridgeVersion   string   `json:"agbridge_version,omitempty"`
	DefaultWorkingDir string   `json:"default_working_dir,omitempty"`
	// ProtocolVersion mirrors handshake.ProtocolVersion so a bridge can
	// detect a pre-working-directory daemon even when an old gateway
	// relays the pairing without checking. Absent (0) means v0.0.4 or
	// older.
	ProtocolVersion uint8 `json:"protocol_version,omitempty"`
}

func (c Capabilities) Encode() ([]byte, error) { return json.Marshal(c) }

func Decode(b []byte) (Capabilities, error) {
	var c Capabilities
	err := json.Unmarshal(b, &c)
	return c, err
}
