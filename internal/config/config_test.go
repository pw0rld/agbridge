package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGatewayMinimal(t *testing.T) {
	yaml := `
listen: 127.0.0.1:8443
audit_path: /tmp/audit.jsonl
agents:
  - name: claude-laptop
    api_key_hash: sha256:abc
    allowed_daemons: [lab01]
daemons:
  - name: lab01
    token_hash: sha256:def
`
	p := writeTemp(t, yaml)
	cfg, err := LoadGateway(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8443" {
		t.Errorf("listen: %q", cfg.Listen)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0].Name != "claude-laptop" {
		t.Errorf("agents: %+v", cfg.Agents)
	}
	if len(cfg.Agents[0].AllowedDaemons) != 1 || cfg.Agents[0].AllowedDaemons[0] != "lab01" {
		t.Errorf("allowed_daemons: %+v", cfg.Agents[0].AllowedDaemons)
	}
	if len(cfg.Daemons) != 1 || cfg.Daemons[0].Name != "lab01" {
		t.Errorf("daemons: %+v", cfg.Daemons)
	}
}

func TestLoadBridge(t *testing.T) {
	yaml := `
gateway_url: wss://gw.example:8443/
agent_name: claude-laptop
api_key: my-api-key
cert_pin: sha256:abc
gateway_tls_mode: webpki
target_daemon: lab01
`
	p := writeTemp(t, yaml)
	cfg, err := LoadBridge(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.GatewayURL != "wss://gw.example:8443/" || cfg.AgentName != "claude-laptop" || cfg.TargetDaemon != "lab01" {
		t.Errorf("got %+v", cfg)
	}
	if cfg.GatewayTLSMode != "webpki" {
		t.Errorf("gateway_tls_mode: got %q", cfg.GatewayTLSMode)
	}
}

func TestLoadDaemon(t *testing.T) {
	yaml := `
gateway_url: wss://gw.example:8443/
daemon_name: lab01
registration_token: my-token
cert_pin: sha256:abc
`
	p := writeTemp(t, yaml)
	cfg, err := LoadDaemon(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DaemonName != "lab01" || cfg.RegistrationToken != "my-token" {
		t.Errorf("got %+v", cfg)
	}
	if cfg.GatewayTLSMode != "pin" {
		t.Errorf("gateway_tls_mode default: got %q", cfg.GatewayTLSMode)
	}
}

func TestLoadBridgeRejectsBadGatewayTLSMode(t *testing.T) {
	yaml := `
gateway_url: wss://gw.example:8443/
agent_name: claude-laptop
api_key: my-api-key
gateway_tls_mode: nope
target_daemon: lab01
`
	p := writeTemp(t, yaml)
	_, err := LoadBridge(p)
	if err == nil || !strings.Contains(err.Error(), "gateway_tls_mode") {
		t.Fatalf("expected gateway_tls_mode error, got %v", err)
	}
}

func TestLoadGatewayRejectsEmpty(t *testing.T) {
	p := writeTemp(t, "")
	_, err := LoadGateway(p)
	if err == nil {
		t.Errorf("expected error for empty config")
	}
	if !strings.Contains(err.Error(), "empty") && !strings.Contains(err.Error(), "listen") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadDaemonWithSandbox(t *testing.T) {
	yaml := `
gateway_url: wss://gw/
daemon_name: lab01
registration_token: tok
cert_pin: sha256:abc
env_allowlist:
  - PATH
  - HOME
  - LANG
forbidden_ports:
  - 22
  - 2375
default_working_dir: /srv/x
`
	p := writeTemp(t, yaml)
	cfg, err := LoadDaemon(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.EnvAllowlist) != 3 {
		t.Errorf("env_allowlist: %+v", cfg.EnvAllowlist)
	}
	if len(cfg.ForbiddenPorts) != 2 || cfg.ForbiddenPorts[0] != 22 {
		t.Errorf("forbidden_ports: %+v", cfg.ForbiddenPorts)
	}
	if cfg.DefaultWorkingDir != "/srv/x" {
		t.Errorf("default_working_dir: %+v", cfg.DefaultWorkingDir)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}
