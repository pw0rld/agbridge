package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/state"
)

func newEnrollCmd() *cobra.Command {
	var (
		gatewayURL string
		token      string
		stateDir   string
		emitJSON   bool
	)
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Enroll this device with a gateway using a one-shot token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if gatewayURL == "" || token == "" {
				return fmt.Errorf("--gateway and --token both required")
			}
			if stateDir == "" {
				stateDir = defaultStateDir()
			}
			return runEnroll(cmd.Context(), gatewayURL, token, stateDir, emitJSON, cmd.OutOrStderr(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway", "", "gateway URL (e.g. wss://gw.example.com/)")
	cmd.Flags().StringVar(&token, "token", "", "enrollment token (et_…)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "where to write state.json + keys (default ~/.config/agbridge/)")
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit machine-readable JSON result")
	return cmd
}

type enrollStep struct {
	Stage     string `json:"stage"`
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
	ElapsedMs int    `json:"elapsed_ms"`
}

type enrollOutcome struct {
	OK        bool         `json:"ok"`
	Stage     string       `json:"stage,omitempty"` // failed stage name (only when !OK)
	Error     string       `json:"error,omitempty"`
	DeviceID  string       `json:"device_id,omitempty"`
	Role      string       `json:"role,omitempty"`
	StatePath string       `json:"state_path,omitempty"`
	Steps     []enrollStep `json:"steps"`
}

func runEnroll(ctx context.Context, gwURL, tokenStr, stateDir string, emitJSON bool, stderr, stdout io.Writer) error {
	outcome := enrollOutcome{}

	report := func(stage string, ok bool, msg string, dur time.Duration) {
		outcome.Steps = append(outcome.Steps, enrollStep{
			Stage: stage, OK: ok, Message: msg, ElapsedMs: int(dur / time.Millisecond),
		})
		if !emitJSON {
			mark := "✓"
			if !ok {
				mark = "✗"
			}
			fmt.Fprintf(stderr, "  [%s] %-20s  %s\n", mark, stage, dur.Round(time.Millisecond))
			if msg != "" {
				fmt.Fprintf(stderr, "        %s\n", msg)
			}
		}
	}
	fail := func(stage string, err error) error {
		outcome.Stage = stage
		outcome.Error = err.Error()
		if emitJSON {
			_ = json.NewEncoder(stdout).Encode(outcome)
		}
		return fmt.Errorf("%s: %w", stage, err)
	}

	u, err := url.Parse(gwURL)
	if err != nil || u.Host == "" {
		return fail("url_parse", fmt.Errorf("invalid --gateway: %v", err))
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	// Stage 1: DNS
	t0 := time.Now()
	dnsAddrs, err := net.DefaultResolver.LookupHost(ctx, hostnameOnly(host))
	if err != nil {
		report("dns_resolve", false, err.Error(), time.Since(t0))
		return fail("dns_resolve", err)
	}
	report("dns_resolve", true, fmt.Sprintf("%d address(es)", len(dnsAddrs)), time.Since(t0))

	// Stage 2: TCP connect
	t0 = time.Now()
	conn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		report("tcp_connect", false, err.Error(), time.Since(t0))
		return fail("tcp_connect", err)
	}
	_ = conn.Close()
	report("tcp_connect", true, "", time.Since(t0))

	// Stage 3: TLS handshake + capture peer cert
	t0 = time.Now()
	tlsConn, err := tls.Dial("tcp", host, &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		report("tls_handshake", false, err.Error(), time.Since(t0))
		return fail("tls_handshake", err)
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	_ = tlsConn.Close()
	if len(certs) == 0 {
		report("tls_handshake", false, "no peer cert", time.Since(t0))
		return fail("tls_handshake", fmt.Errorf("no peer cert"))
	}
	report("tls_handshake", true, "TLS 1.3 OK", time.Since(t0))

	// Stage 4: cert pin (computed locally; the gateway returns its
	// authoritative pin in the enroll response — we compare in stage 7.)
	t0 = time.Now()
	pinBytes := sha256.Sum256(certs[0].Raw)
	pin := "sha256:" + hex.EncodeToString(pinBytes[:])
	report("cert_pin", true, pin, time.Since(t0))

	// Stage 5: HTTP reachability — POST empty body, expect 400 (handler
	// is wired) rather than 404 (handler missing).
	t0 = time.Now()
	httpsURL := "https://" + host + "/v1/enroll"
	cl := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}},
		Timeout:   5 * time.Second,
	}
	probeResp, err := cl.Post(httpsURL, "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		report("http_reachable", false, err.Error(), time.Since(t0))
		return fail("http_reachable", err)
	}
	_ = probeResp.Body.Close()
	if probeResp.StatusCode != http.StatusBadRequest && probeResp.StatusCode != http.StatusUnauthorized {
		report("http_reachable", false, fmt.Sprintf("status %d (expected 400/401)", probeResp.StatusCode), time.Since(t0))
		return fail("http_reachable", fmt.Errorf("status %d", probeResp.StatusCode))
	}
	report("http_reachable", true, "", time.Since(t0))

	// Generate device noise keypair locally.
	devKey, err := e2e.GenerateStatic()
	if err != nil {
		return fail("noise_keygen", err)
	}

	// Stage 6: token exchange
	t0 = time.Now()
	reqBody, _ := json.Marshal(gateway.EnrollRequest{
		Token:           tokenStr,
		DevicePubkeyB64: devKey.PubBase64(),
	})
	resp, err := cl.Post(httpsURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		report("token_exchange", false, err.Error(), time.Since(t0))
		return fail("token_exchange", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		msg := strings.TrimSpace(buf.String())
		report("token_exchange", false, fmt.Sprintf("status %d: %s", resp.StatusCode, msg), time.Since(t0))
		return fail("token_exchange", fmt.Errorf("status %d: %s", resp.StatusCode, msg))
	}
	var enrollResp gateway.EnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&enrollResp); err != nil {
		report("token_exchange", false, err.Error(), time.Since(t0))
		return fail("token_exchange", err)
	}
	// Cross-check the gateway-supplied pin against what we saw on the
	// wire. They MUST match — otherwise we are talking to two different
	// TLS terminators (MITM).
	if enrollResp.CertPin != pin {
		report("token_exchange", false, fmt.Sprintf("pin mismatch: local=%s remote=%s", pin, enrollResp.CertPin), time.Since(t0))
		return fail("token_exchange", fmt.Errorf("cert pin mismatch"))
	}
	role := "bridge"
	if enrollResp.TargetDaemon == "" {
		role = "daemon"
	}
	report("token_exchange", true, fmt.Sprintf("device_id=%s role=%s", enrollResp.DeviceID, role), time.Since(t0))

	// Stage 7: save state.json + key file
	t0 = time.Now()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		report("save_state", false, err.Error(), time.Since(t0))
		return fail("save_state", err)
	}
	keyPath := filepath.Join(stateDir, role+"_noise.key")
	if err := devKey.Save(keyPath); err != nil {
		report("save_state", false, err.Error(), time.Since(t0))
		return fail("save_state", err)
	}

	statePath := filepath.Join(stateDir, "state.json")
	switch role {
	case "bridge":
		bs := &state.BridgeState{
			Version:             state.CurrentVersion,
			DeviceID:            enrollResp.DeviceID,
			AgentName:           enrollResp.Name,
			APIKey:              enrollResp.APIKey,
			Gateway:             state.GatewayRef{URL: gwURL, CertPin: enrollResp.CertPin},
			BridgeStaticKeyPath: keyPath,
			E2EMode:             enrollResp.E2EMode,
			DefaultTarget:       enrollResp.TargetDaemon,
			KnownDaemons: map[string]state.DaemonRef{
				enrollResp.TargetDaemon: {
					Pubkey:    enrollResp.DaemonPubkey,
					PinSource: "control-plane",
					FirstSeen: time.Now(),
				},
			},
			EnrolledAt: time.Now(),
		}
		if err := state.SaveBridge(statePath, bs); err != nil {
			report("save_state", false, err.Error(), time.Since(t0))
			return fail("save_state", err)
		}
	case "daemon":
		ds := &state.DaemonState{
			Version:              state.CurrentVersion,
			DeviceID:             enrollResp.DeviceID,
			DaemonName:           enrollResp.Name,
			APIKey:               enrollResp.APIKey,
			Gateway:              state.GatewayRef{URL: gwURL, CertPin: enrollResp.CertPin},
			NoiseStaticKeyPath:   keyPath,
			E2EMode:              enrollResp.E2EMode,
			AllowedExecCwds:      enrollResp.AllowedExecCwds,
			AllowedReadPaths:     enrollResp.AllowedReadPaths,
			AllowedWritePaths:    enrollResp.AllowedWritePaths,
			EnvAllowlist:         enrollResp.EnvAllowlist,
			ForbiddenPorts:       enrollResp.ForbiddenPorts,
			AllowedBridgePubkeys: enrollResp.AllowedBridgePubkeys,
			EnrolledAt:           time.Now(),
		}
		if err := state.SaveDaemon(statePath, ds); err != nil {
			report("save_state", false, err.Error(), time.Since(t0))
			return fail("save_state", err)
		}
	}
	report("save_state", true, statePath, time.Since(t0))

	outcome.OK = true
	outcome.DeviceID = enrollResp.DeviceID
	outcome.Role = role
	outcome.StatePath = statePath
	if emitJSON {
		return json.NewEncoder(stdout).Encode(outcome)
	}
	fmt.Fprintf(stderr, "\nEnrolled as %q (role=%s).\n", enrollResp.Name, role)
	fmt.Fprintf(stderr, "Start the %s: agbridge %s --state-dir %s\n", role, role, stateDir)
	return nil
}

func defaultStateDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "agbridge")
	}
	return "."
}

func hostnameOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[:i]
	}
	return addr
}
