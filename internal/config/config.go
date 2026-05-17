// Package config loads YAML configuration for gateway, bridge, and daemon.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// GatewayConfig drives "agbridge gateway".
type GatewayConfig struct {
	Listen          string        `yaml:"listen"`
	PublicURL       string        `yaml:"public_url"` // e.g. wss://gw.example.com/ — surfaced in issue-token output
	AuditPath       string        `yaml:"audit_path"`
	AuditMaxBytes   int64         `yaml:"audit_max_bytes"`
	AuditMaxBackups int           `yaml:"audit_max_backups"`
	Agents          []AgentEntry  `yaml:"agents"`
	Daemons         []DaemonEntry `yaml:"daemons"`
}

// AgentEntry is one row under gateway.agents.
type AgentEntry struct {
	Name           string   `yaml:"name"`
	APIKeyHash     string   `yaml:"api_key_hash"`
	AllowedDaemons []string `yaml:"allowed_daemons"`
}

// DaemonEntry is one row under gateway.daemons.
type DaemonEntry struct {
	Name      string `yaml:"name"`
	TokenHash string `yaml:"token_hash"`
}

// BridgeConfig drives "agbridge bridge".
type BridgeConfig struct {
	GatewayURL   string `yaml:"gateway_url"`
	AgentName    string `yaml:"agent_name"`
	APIKey       string `yaml:"api_key"`
	CertPin      string `yaml:"cert_pin"`
	TargetDaemon string `yaml:"target_daemon"`

	// E2E (v0.1.0+). Default "disabled" preserves pre-v0.1.0 behavior.
	E2EMode             string `yaml:"e2e_mode"`               // "disabled" | "optional" | "required"
	BridgeStaticKeyPath string `yaml:"bridge_static_key_path"` // path to X25519 priv (32B)
	DaemonPubkey        string `yaml:"daemon_pubkey"`          // base64 X25519 pub of target daemon
}

// DaemonConfig drives "agbridge daemon".
type DaemonConfig struct {
	GatewayURL        string   `yaml:"gateway_url"`
	DaemonName        string   `yaml:"daemon_name"`
	RegistrationToken string   `yaml:"registration_token"`
	CertPin           string   `yaml:"cert_pin"`
	AllowedExecCwds   []string `yaml:"allowed_exec_cwds"`
	EnvAllowlist      []string `yaml:"env_allowlist"`
	AllowedReadPaths  []string `yaml:"allowed_read_paths"`
	AllowedWritePaths []string `yaml:"allowed_write_paths"`
	ForbiddenPorts    []int    `yaml:"forbidden_ports"`

	// E2E (v0.1.0+).
	E2EMode              string   `yaml:"e2e_mode"`               // "disabled" | "optional" | "required"
	NoiseStaticKeyPath   string   `yaml:"noise_static_key_path"`  // path to X25519 priv (32B)
	AllowedBridgePubkeys []string `yaml:"allowed_bridge_pubkeys"` // base64 X25519 pubs; required if e2e_mode=required
}

// LoadGateway parses a gateway YAML config from path and validates it.
func LoadGateway(path string) (*GatewayConfig, error) {
	var cfg GatewayConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if cfg.Listen == "" {
		return nil, errors.New("config: listen address is required")
	}
	if cfg.AuditPath == "" {
		return nil, errors.New("config: audit_path is required")
	}
	return &cfg, nil
}

// LoadBridge parses a bridge YAML config.
func LoadBridge(path string) (*BridgeConfig, error) {
	var cfg BridgeConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if cfg.GatewayURL == "" || cfg.AgentName == "" || cfg.APIKey == "" || cfg.TargetDaemon == "" {
		return nil, errors.New("config: gateway_url, agent_name, api_key, target_daemon all required")
	}
	if err := normalizeBridgeE2E(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadDaemon parses a daemon YAML config.
func LoadDaemon(path string) (*DaemonConfig, error) {
	var cfg DaemonConfig
	if err := loadYAML(path, &cfg); err != nil {
		return nil, err
	}
	if cfg.GatewayURL == "" || cfg.DaemonName == "" || cfg.RegistrationToken == "" {
		return nil, errors.New("config: gateway_url, daemon_name, registration_token all required")
	}
	if err := normalizeDaemonE2E(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func normalizeBridgeE2E(cfg *BridgeConfig) error {
	switch cfg.E2EMode {
	case "":
		cfg.E2EMode = "disabled"
	case "disabled", "optional", "required":
		// ok
	default:
		return fmt.Errorf("config: invalid e2e_mode %q (want disabled|optional|required)", cfg.E2EMode)
	}
	if cfg.E2EMode == "disabled" {
		return nil
	}
	if cfg.BridgeStaticKeyPath == "" {
		return fmt.Errorf("config: bridge_static_key_path required when e2e_mode=%s", cfg.E2EMode)
	}
	if cfg.DaemonPubkey == "" {
		return fmt.Errorf("config: daemon_pubkey required when e2e_mode=%s", cfg.E2EMode)
	}
	return nil
}

func normalizeDaemonE2E(cfg *DaemonConfig) error {
	switch cfg.E2EMode {
	case "":
		cfg.E2EMode = "disabled"
	case "disabled", "optional", "required":
		// ok
	default:
		return fmt.Errorf("config: invalid e2e_mode %q (want disabled|optional|required)", cfg.E2EMode)
	}
	if cfg.E2EMode == "disabled" {
		return nil
	}
	if cfg.NoiseStaticKeyPath == "" {
		return fmt.Errorf("config: noise_static_key_path required when e2e_mode=%s", cfg.E2EMode)
	}
	if cfg.E2EMode == "required" && len(cfg.AllowedBridgePubkeys) == 0 {
		return errors.New("config: allowed_bridge_pubkeys must be non-empty when e2e_mode=required (refusing to start with empty allowlist)")
	}
	return nil
}

func loadYAML(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(b) == 0 {
		return errors.New("config: file is empty")
	}
	if err := yaml.Unmarshal(b, out); err != nil {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}
