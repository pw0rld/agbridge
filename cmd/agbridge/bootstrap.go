package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/pki"
)

// newBootstrapCmd produces a complete agbridge config tree (cert + 3 YAMLs)
// in one shot. Defaults are tuned for a single-agent / single-daemon setup;
// users with multiple agents or daemons should edit the generated
// gateway.yaml after the fact.
func newBootstrapCmd() *cobra.Command {
	var (
		gatewayURL    string
		gatewayListen string
		cn            string
		agent         string
		daemon        string
		target        string
		allowedPaths  []string
		auditPath     string
		days          int
		outDir        string
		emitJSON      bool
		e2eEnabled    bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Generate cert + gateway.yaml + daemon.yaml + bridge.yaml in one shot",
		Long: `bootstrap generates everything needed to stand up a 3-machine agbridge
deployment:

  - cert.pem + key.pem  (deploy to gateway machine)
  - gateway.yaml        (deploy to gateway machine)
  - daemon.yaml         (deploy to daemon machine)
  - bridge.yaml         (stays on the laptop running the MCP client)

Hashes and pins are aligned automatically. Run once, scp the right files
to the right hosts, and start the three processes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if gatewayURL == "" {
				return fmt.Errorf("--gateway-url is required (e.g., wss://gw.example.com/)")
			}
			if target == "" {
				target = daemon
			}
			if cn == "" {
				cn = hostFromURL(gatewayURL)
			}

			cert, err := pki.GenerateSelfSigned(pki.Options{CommonName: cn, ValidDays: days})
			if err != nil {
				return fmt.Errorf("generate cert: %w", err)
			}
			apiKey, err := randomSecret()
			if err != nil {
				return fmt.Errorf("api key: %w", err)
			}
			daemonTok, err := randomSecret()
			if err != nil {
				return fmt.Errorf("daemon token: %w", err)
			}
			apiKeyHash := "sha256:" + auth.SHA256Hex([]byte(apiKey))
			daemonTokHash := "sha256:" + auth.SHA256Hex([]byte(daemonTok))

			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", outDir, err)
			}

			certPath := filepath.Join(outDir, "cert.pem")
			keyPath := filepath.Join(outDir, "key.pem")
			gatewayCfg := filepath.Join(outDir, "gateway.yaml")
			daemonCfg := filepath.Join(outDir, "daemon.yaml")
			bridgeCfg := filepath.Join(outDir, "bridge.yaml")

			if err := os.WriteFile(certPath, cert.CertPEM, 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(keyPath, cert.KeyPEM, 0o600); err != nil {
				return err
			}

			// Optional Noise keypairs for E2E (v0.1.0+). Empty strings → renderers
			// emit no e2e_mode block → daemon/bridge defaults to "disabled".
			var bridgeKeyPath, daemonKeyPath, bridgePubB64, daemonPubB64 string
			if e2eEnabled {
				bk, err := e2e.GenerateStatic()
				if err != nil {
					return fmt.Errorf("generate bridge noise keypair: %w", err)
				}
				dk, err := e2e.GenerateStatic()
				if err != nil {
					return fmt.Errorf("generate daemon noise keypair: %w", err)
				}
				bridgeKeyPath = filepath.Join(outDir, "bridge_noise.key")
				daemonKeyPath = filepath.Join(outDir, "daemon_noise.key")
				if err := bk.Save(bridgeKeyPath); err != nil {
					return fmt.Errorf("save bridge noise key: %w", err)
				}
				if err := dk.Save(daemonKeyPath); err != nil {
					return fmt.Errorf("save daemon noise key: %w", err)
				}
				bridgePubB64 = bk.PubBase64()
				daemonPubB64 = dk.PubBase64()
			}

			if err := os.WriteFile(gatewayCfg, []byte(renderGatewayYAML(gatewayListen, auditPath, agent, apiKeyHash, daemon, daemonTokHash)), 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(daemonCfg, []byte(renderDaemonYAML(gatewayURL, daemon, daemonTok, cert.Pin, allowedPaths, daemonKeyPath, bridgePubB64)), 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(bridgeCfg, []byte(renderBridgeYAML(gatewayURL, agent, apiKey, cert.Pin, target, bridgeKeyPath, daemonPubB64)), 0o600); err != nil {
				return err
			}

			result := bootstrapResult{
				CertPath:           certPath,
				KeyPath:            keyPath,
				GatewayCfg:         gatewayCfg,
				DaemonCfg:          daemonCfg,
				BridgeCfg:          bridgeCfg,
				CertPin:            cert.Pin,
				AgentName:          agent,
				DaemonName:         daemon,
				APIKey:             apiKey,
				DaemonToken:        daemonTok,
				GatewayURL:         gatewayURL,
				E2EEnabled:         e2eEnabled,
				BridgeNoiseKey:     bridgeKeyPath,
				DaemonNoiseKey:     daemonKeyPath,
				BridgeNoisePubB64:  bridgePubB64,
				DaemonNoisePubB64:  daemonPubB64,
			}

			if emitJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			fmt.Fprint(cmd.OutOrStdout(), result.humanSummary())
			return nil
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "wss URL daemons + bridges dial (e.g., wss://gw.example.com/)")
	cmd.Flags().StringVar(&gatewayListen, "gateway-listen", "0.0.0.0:443", "address gateway binds")
	cmd.Flags().StringVar(&cn, "cn", "", "TLS cert CN (defaults to host from --gateway-url)")
	cmd.Flags().StringVar(&agent, "agent", "claude-laptop", "bridge agent name")
	cmd.Flags().StringVar(&daemon, "daemon", "lab01", "daemon name")
	cmd.Flags().StringVar(&target, "target", "", "daemon the bridge targets (default: --daemon)")
	cmd.Flags().StringSliceVar(&allowedPaths, "allowed-paths", []string{"/tmp/agbridge-demo"}, "paths the daemon may exec / read / write (repeatable, prefix-glob with trailing /*)")
	cmd.Flags().StringVar(&auditPath, "audit-path", "./audit.jsonl", "gateway audit log path")
	cmd.Flags().IntVar(&days, "days", 365, "TLS cert validity in days")
	cmd.Flags().StringVar(&outDir, "out", "./agbridge-bootstrap", "output directory")
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit machine-readable JSON")
	cmd.Flags().BoolVar(&e2eEnabled, "e2e", true, "enable Noise IK E2E (generates X25519 keypairs and sets e2e_mode=required)")
	return cmd
}

// bootstrapResult is the structured output of `bootstrap --json`. The
// plaintext APIKey / DaemonToken are included since the user (or AI agent)
// needs them to set up additional bridges/daemons later.
type bootstrapResult struct {
	CertPath          string `json:"cert_path"`
	KeyPath           string `json:"key_path"`
	GatewayCfg        string `json:"gateway_cfg"`
	DaemonCfg         string `json:"daemon_cfg"`
	BridgeCfg         string `json:"bridge_cfg"`
	CertPin           string `json:"cert_pin"`
	AgentName         string `json:"agent_name"`
	DaemonName        string `json:"daemon_name"`
	APIKey            string `json:"api_key"`
	DaemonToken       string `json:"daemon_token"`
	GatewayURL        string `json:"gateway_url"`
	E2EEnabled        bool   `json:"e2e_enabled"`
	BridgeNoiseKey    string `json:"bridge_noise_key,omitempty"`
	DaemonNoiseKey    string `json:"daemon_noise_key,omitempty"`
	BridgeNoisePubB64 string `json:"bridge_noise_pub_b64,omitempty"`
	DaemonNoisePubB64 string `json:"daemon_noise_pub_b64,omitempty"`
}

func (r bootstrapResult) humanSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Bootstrap complete.\n\n")
	fmt.Fprintf(&b, "Generated:\n")
	fmt.Fprintf(&b, "  %s\n  %s\n  %s\n  %s\n  %s\n\n",
		r.CertPath, r.KeyPath, r.GatewayCfg, r.DaemonCfg, r.BridgeCfg)
	fmt.Fprintf(&b, "cert_pin: %s\n", r.CertPin)
	fmt.Fprintf(&b, "agent: %s   daemon: %s\n\n", r.AgentName, r.DaemonName)
	fmt.Fprintf(&b, "Next steps:\n")
	fmt.Fprintf(&b, "  # On the gateway host\n")
	fmt.Fprintf(&b, "  scp %s %s %s <gateway>:/etc/agbridge/\n", r.CertPath, r.KeyPath, r.GatewayCfg)
	fmt.Fprintf(&b, "  agbridge gateway --config /etc/agbridge/gateway.yaml --cert /etc/agbridge/cert.pem --key /etc/agbridge/key.pem\n\n")
	fmt.Fprintf(&b, "  # On the daemon host (run as non-root)\n")
	fmt.Fprintf(&b, "  scp %s <daemon>:/etc/agbridge/\n", r.DaemonCfg)
	fmt.Fprintf(&b, "  agbridge daemon --config /etc/agbridge/daemon.yaml\n\n")
	fmt.Fprintf(&b, "  # On the bridge host: register with your MCP client (Claude Code etc.)\n")
	fmt.Fprintf(&b, "  {\n")
	fmt.Fprintf(&b, "    \"mcpServers\": {\n")
	fmt.Fprintf(&b, "      \"agbridge\": {\n")
	fmt.Fprintf(&b, "        \"command\": \"agbridge\",\n")
	fmt.Fprintf(&b, "        \"args\": [\"bridge\", \"--config\", \"%s\"]\n", r.BridgeCfg)
	fmt.Fprintf(&b, "      }\n")
	fmt.Fprintf(&b, "    }\n")
	fmt.Fprintf(&b, "  }\n")
	return b.String()
}

// randomSecret returns 32 random bytes base64-encoded — same shape as
// `agbridge keygen`'s secret field.
func randomSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw[:]), nil
}

// hostFromURL pulls the host (without port) from gatewayURL, falling back
// to "agbridge" if unparseable.
func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "agbridge"
	}
	return u.Hostname()
}

func renderGatewayYAML(listen, auditPath, agent, apiKeyHash, daemon, daemonTokHash string) string {
	return fmt.Sprintf(`listen: %s
audit_path: %s
agents:
  - name: %s
    api_key_hash: %s
    allowed_daemons: [%s]
daemons:
  - name: %s
    token_hash: %s
`, listen, auditPath, agent, apiKeyHash, daemon, daemon, daemonTokHash)
}

func renderDaemonYAML(gatewayURL, daemonName, daemonTok, certPin string, allowedPaths []string, noiseKeyPath, bridgePubB64 string) string {
	var paths strings.Builder
	for _, p := range allowedPaths {
		// Include both the exact path and a prefix-glob for descendants.
		fmt.Fprintf(&paths, "  - %s\n", p)
		fmt.Fprintf(&paths, "  - %s/*\n", strings.TrimRight(p, "/"))
	}
	var e2eBlock string
	if noiseKeyPath != "" && bridgePubB64 != "" {
		e2eBlock = fmt.Sprintf(`e2e_mode: required
noise_static_key_path: %s
allowed_bridge_pubkeys:
  - %q
`, noiseKeyPath, bridgePubB64)
	}
	return fmt.Sprintf(`gateway_url: %s
daemon_name: %s
registration_token: %s
cert_pin: %s
allowed_exec_cwds:
%sallowed_read_paths:
%sallowed_write_paths:
%senv_allowlist:
  - PATH
  - HOME
  - LANG
%s`, gatewayURL, daemonName, daemonTok, certPin, paths.String(), paths.String(), paths.String(), e2eBlock)
}

func renderBridgeYAML(gatewayURL, agent, apiKey, certPin, target, bridgeKeyPath, daemonPubB64 string) string {
	var e2eBlock string
	if bridgeKeyPath != "" && daemonPubB64 != "" {
		e2eBlock = fmt.Sprintf(`e2e_mode: required
bridge_static_key_path: %s
daemon_pubkey: %q
`, bridgeKeyPath, daemonPubB64)
	}
	return fmt.Sprintf(`gateway_url: %s
agent_name: %s
api_key: %s
cert_pin: %s
target_daemon: %s
%s`, gatewayURL, agent, apiKey, certPin, target, e2eBlock)
}
