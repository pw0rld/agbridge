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
	Listen    string        `yaml:"listen"`
	AuditPath string        `yaml:"audit_path"`
	Agents    []AgentEntry  `yaml:"agents"`
	Daemons   []DaemonEntry `yaml:"daemons"`
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
	return &cfg, nil
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
