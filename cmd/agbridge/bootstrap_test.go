package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/pki"
)

// runBootstrap invokes `agbridge bootstrap --json` and returns the parsed
// result plus the on-disk output dir.
func runBootstrap(t *testing.T, extraArgs ...string) (bootstrapResult, string) {
	t.Helper()
	bin := ensureBinary(t)
	dir := t.TempDir()
	args := []string{
		"bootstrap",
		"--gateway-url", "wss://gw.example.com/",
		"--agent", "claude-laptop",
		"--daemon", "lab01",
		"--allowed-paths", filepath.Join(dir, "scratch"),
		"--audit-path", filepath.Join(dir, "audit.jsonl"),
		"--out", filepath.Join(dir, "out"),
		"--json",
	}
	args = append(args, extraArgs...)
	stdout, err := exec.Command(bin, args...).Output()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var got bootstrapResult
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("decode bootstrap json: %v: %q", err, stdout)
	}
	return got, filepath.Join(dir, "out")
}

func TestBootstrapWritesAllFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	got, outDir := runBootstrap(t)
	for _, name := range []string{"cert.pem", "key.pem", "gateway.yaml", "daemon.yaml", "bridge.yaml"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if got.AgentName != "claude-laptop" || got.DaemonName != "lab01" {
		t.Errorf("names: %+v", got)
	}
}

// TestBootstrapConfigsParse runs each generated YAML through the real
// config.Load* functions. Catches YAML schema drift.
func TestBootstrapConfigsParse(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	got, _ := runBootstrap(t)

	if _, err := config.LoadGateway(got.GatewayCfg); err != nil {
		t.Errorf("LoadGateway: %v", err)
	}
	if _, err := config.LoadDaemon(got.DaemonCfg); err != nil {
		t.Errorf("LoadDaemon: %v", err)
	}
	if _, err := config.LoadBridge(got.BridgeCfg); err != nil {
		t.Errorf("LoadBridge: %v", err)
	}
}

// TestBootstrapCrossConfigAlignment verifies that the cert pin, api key
// hash, and daemon token hash in the three YAMLs reference the same
// secrets. A typo in renderGatewayYAML / renderBridgeYAML / renderDaemonYAML
// would surface here.
func TestBootstrapCrossConfigAlignment(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	got, _ := runBootstrap(t)

	gwCfg, err := config.LoadGateway(got.GatewayCfg)
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	dmCfg, err := config.LoadDaemon(got.DaemonCfg)
	if err != nil {
		t.Fatalf("LoadDaemon: %v", err)
	}
	brCfg, err := config.LoadBridge(got.BridgeCfg)
	if err != nil {
		t.Fatalf("LoadBridge: %v", err)
	}

	if dmCfg.CertPin != got.CertPin || brCfg.CertPin != got.CertPin {
		t.Errorf("cert pin mismatch: gateway=%s daemon=%s bridge=%s",
			got.CertPin, dmCfg.CertPin, brCfg.CertPin)
	}
	expectedAgentHash := "sha256:" + auth.SHA256Hex([]byte(brCfg.APIKey))
	if gwCfg.Agents[0].APIKeyHash != expectedAgentHash {
		t.Errorf("api_key hash mismatch: gateway=%s expected=%s",
			gwCfg.Agents[0].APIKeyHash, expectedAgentHash)
	}
	expectedDaemonHash := "sha256:" + auth.SHA256Hex([]byte(dmCfg.RegistrationToken))
	if gwCfg.Daemons[0].TokenHash != expectedDaemonHash {
		t.Errorf("daemon token hash mismatch: gateway=%s expected=%s",
			gwCfg.Daemons[0].TokenHash, expectedDaemonHash)
	}

	certPEM, _ := os.ReadFile(filepath.Join(filepath.Dir(got.GatewayCfg), "cert.pem"))
	gotPin, _ := pki.PinFromCertPEM(certPEM)
	if gotPin != got.CertPin {
		t.Errorf("on-disk cert pin mismatch: got %s vs reported %s", gotPin, got.CertPin)
	}
}

func TestBootstrapHumanOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	bin := ensureBinary(t)
	dir := t.TempDir()
	out, err := exec.Command(bin,
		"bootstrap",
		"--gateway-url", "wss://gw.example.com/",
		"--allowed-paths", filepath.Join(dir, "scratch"),
		"--audit-path", filepath.Join(dir, "audit.jsonl"),
		"--out", filepath.Join(dir, "out"),
	).Output()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	stdout := string(out)
	for _, want := range []string{"Bootstrap complete", "cert.pem", "gateway.yaml", "daemon.yaml", "bridge.yaml", "mcpServers"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q:\n%s", want, stdout)
		}
	}
}

func TestBootstrapDefaultCNFromURL(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	// Default --cn extracts host from --gateway-url.
	bin := ensureBinary(t)
	dir := t.TempDir()
	out, err := exec.Command(bin,
		"bootstrap",
		"--gateway-url", "wss://example-host.test:443/",
		"--out", filepath.Join(dir, "out"),
		"--json",
	).Output()
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var got bootstrapResult
	_ = json.Unmarshal(out, &got)
	certPEM, _ := os.ReadFile(got.CertPath)
	pin, _ := pki.PinFromCertPEM(certPEM)
	if pin != got.CertPin {
		t.Errorf("pin mismatch")
	}
	// Spot-check the cert was generated with the host as CN.
	if !strings.Contains(string(certPEM), "BEGIN CERTIFICATE") {
		t.Errorf("cert not a PEM: %q", certPEM[:40])
	}
}
