// Package state defines the JSON schemas backing agbridge's persistent
// per-device configuration. v0.1.0 Phase B replaces the legacy yaml
// configs with one state.json per role:
//
//	bridge:  ~/.config/agbridge/state.json
//	daemon:  /etc/agbridge/state.json
//	gateway: /etc/agbridge/state.json (auto-managed alongside operator yaml)
//
// Each state file is 0600 plain JSON. Private keys live in separate
// 0600 files referenced by *KeyPath fields. Users never edit these by
// hand; agbridge enroll / agbridge config write them.
package state

import "time"

// CurrentVersion identifies the schema generation. Bumped on incompatible
// changes so older binaries refuse to load newer state.
const CurrentVersion = 1

// GatewayRef is the gateway address + cert pin a device dials.
type GatewayRef struct {
	URL     string `json:"url"`
	CertPin string `json:"cert_pin"`
}

// DaemonRef is the pinned identity of one daemon as cached on a bridge.
type DaemonRef struct {
	DeviceID  string    `json:"device_id"`
	Pubkey    string    `json:"pubkey"`     // base64 X25519
	PinSource string    `json:"pin_source"` // "control-plane" | "tofu" | "manual"
	FirstSeen time.Time `json:"first_seen"`
	Alias     string    `json:"alias,omitempty"`
}

// BridgeState lives on the laptop where the MCP client runs.
type BridgeState struct {
	Version             int                  `json:"version"`
	DeviceID            string               `json:"device_id"`
	AgentName           string               `json:"agent_name"`
	APIKey              string               `json:"api_key"`
	Gateway             GatewayRef           `json:"gateway"`
	BridgeStaticKeyPath string               `json:"bridge_static_key_path"`
	E2EMode             string               `json:"e2e_mode"`
	DefaultTarget       string               `json:"default_target"`
	KnownDaemons        map[string]DaemonRef `json:"known_daemons"`
	EnrolledAt          time.Time            `json:"enrolled_at"`
}

// DaemonState lives on the daemon host.
type DaemonState struct {
	Version              int        `json:"version"`
	DeviceID             string     `json:"device_id"`
	DaemonName           string     `json:"daemon_name"`
	APIKey               string     `json:"api_key"`
	Gateway              GatewayRef `json:"gateway"`
	NoiseStaticKeyPath   string     `json:"noise_static_key_path"`
	E2EMode              string     `json:"e2e_mode"`
	AllowedExecCwds      []string   `json:"allowed_exec_cwds"`
	EnvAllowlist         []string   `json:"env_allowlist"`
	AllowedReadPaths     []string   `json:"allowed_read_paths"`
	AllowedWritePaths    []string   `json:"allowed_write_paths"`
	ForbiddenPorts       []int      `json:"forbidden_ports"`
	AllowedBridgePubkeys []string   `json:"allowed_bridge_pubkeys"`
	EnrolledAt           time.Time  `json:"enrolled_at"`
}

// GatewayState lives on the gateway host. Replaces gateway.yaml.
// Unlike bridge/daemon state, this one is edited indirectly by the
// gateway when issue-token / enroll calls register new principals.
type GatewayState struct {
	Version         int           `json:"version"`
	Listen          string        `json:"listen"`
	AuditPath       string        `json:"audit_path"`
	AuditMaxBytes   int64         `json:"audit_max_bytes"`
	AuditMaxBackups int           `json:"audit_max_backups"`
	TokensPath      string        `json:"tokens_path"`
	Agents          []AgentEntry  `json:"agents"`
	Daemons         []DaemonEntry `json:"daemons"`
}

// AgentEntry is one bridge known to the gateway.
type AgentEntry struct {
	Name           string   `json:"name"`
	DeviceID       string   `json:"device_id"`
	APIKeyHash     string   `json:"api_key_hash"`
	AllowedDaemons []string `json:"allowed_daemons"`
}

// DaemonEntry is one daemon known to the gateway.
type DaemonEntry struct {
	Name      string `json:"name"`
	DeviceID  string `json:"device_id"`
	TokenHash string `json:"token_hash"`
	NoisePub  string `json:"noise_pub,omitempty"` // base64 X25519, populated after enroll
}
