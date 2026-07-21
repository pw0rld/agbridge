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

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/e2e"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/state"
)

func newEnrollCmd() *cobra.Command {
	var (
		gatewayURL  string
		token       string
		stateDir    string
		tlsMode     string
		serviceUser string
		noService   bool
		emitJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Enroll this device with a gateway using a one-shot token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if gatewayURL == "" || token == "" {
				return fmt.Errorf("--gateway and --token both required")
			}
			// Leave stateDir empty when not given; runEnroll defaults it
			// once the role + service user are known (a root-installed
			// daemon service defaults its state under the service user's
			// home, not root's).
			return runEnroll(cmd.Context(), gatewayURL, token, stateDir, tlsMode, serviceUser, !noService, emitJSON, cmd.OutOrStderr(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&gatewayURL, "gateway", "", "gateway URL (e.g. wss://gw.example.com/)")
	cmd.Flags().StringVar(&token, "token", "", "enrollment token (et_…)")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "where to write state.json + keys (default ~/.config/agbridge/)")
	cmd.Flags().StringVar(&tlsMode, "tls-mode", "pin", "gateway TLS mode: pin (origin cert pin) or webpki (system CA, Cloudflare-compatible)")
	cmd.Flags().StringVar(&serviceUser, "service-user", "", "daemon-only, root install: OS user the daemon service runs as (default: auto-detect a non-root account)")
	cmd.Flags().BoolVar(&noService, "no-start-service", false, "daemon-only: skip installing/starting the systemd service")
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
	OK           bool         `json:"ok"`
	Stage        string       `json:"stage,omitempty"` // failed stage name (only when !OK)
	Error        string       `json:"error,omitempty"`
	DeviceID     string       `json:"device_id,omitempty"`
	Role         string       `json:"role,omitempty"`
	StatePath    string       `json:"state_path,omitempty"`
	Service      string       `json:"service,omitempty"`      // installed unit name (daemon)
	ServiceMode  string       `json:"service_mode,omitempty"` // "system" | "user"
	ServiceUser  string       `json:"service_user,omitempty"` // system mode: the run-as account
	ServiceOK    bool         `json:"service_ok,omitempty"`
	ServiceError string       `json:"service_error,omitempty"`
	Steps        []enrollStep `json:"steps"`
}

func runEnroll(ctx context.Context, gwURL, tokenStr, stateDir, tlsMode, serviceUser string, startService bool, emitJSON bool, stderr, stdout io.Writer) error {
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

	tlsMode, err := auth.NormalizeGatewayTLSMode(tlsMode)
	if err != nil {
		return fail("tls_mode", err)
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
	tlsCfg, err := auth.EnrollTLSConfig(tlsMode)
	if err != nil {
		report("tls_handshake", false, err.Error(), time.Since(t0))
		return fail("tls_handshake", err)
	}
	tlsConn, err := tls.Dial("tcp", host, tlsCfg)
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
	report("tls_handshake", true, fmt.Sprintf("TLS 1.3 OK (%s)", tlsMode), time.Since(t0))

	// Stage 4: cert pin (computed locally; the gateway returns its
	// authoritative pin in the enroll response — we compare in stage 7.)
	t0 = time.Now()
	pinBytes := sha256.Sum256(certs[0].Raw)
	pin := "sha256:" + hex.EncodeToString(pinBytes[:])
	if tlsMode == auth.GatewayTLSModeWebPKI {
		report("cert_pin", true, fmt.Sprintf("%s (diagnostic only; webpki mode)", pin), time.Since(t0))
	} else {
		report("cert_pin", true, pin, time.Since(t0))
	}

	// Stage 5: HTTP reachability — POST empty body, expect 400 (handler
	// is wired) rather than 404 (handler missing).
	t0 = time.Now()
	httpsURL := "https://" + host + "/v1/enroll"
	cl := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg.Clone()},
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
	if tlsMode == auth.GatewayTLSModePin && enrollResp.CertPin != pin {
		report("token_exchange", false, fmt.Sprintf("pin mismatch: local=%s remote=%s", pin, enrollResp.CertPin), time.Since(t0))
		return fail("token_exchange", fmt.Errorf("cert pin mismatch"))
	}
	role := "bridge"
	if enrollResp.TargetDaemon == "" {
		role = "daemon"
	}
	report("token_exchange", true, fmt.Sprintf("device_id=%s role=%s", enrollResp.DeviceID, role), time.Since(t0))

	// Decide how the daemon service will be installed (and thus where
	// state should live) BEFORE writing state: a root-installed system
	// service runs as a non-root user and defaults its state under that
	// user's home, not /root — which the service user can't traverse.
	stateDirExplicit := stateDir != ""
	var svcPlan daemonServicePlan
	var svcPlanErr error
	if role == "daemon" && startService {
		svcPlan, svcPlanErr = planDaemonService(os.Geteuid(), serviceUser, stateDir, stateDirExplicit, osUserLookup{})
		if svcPlanErr == nil && svcPlan.StateDir != "" {
			stateDir = svcPlan.StateDir
		}
	}
	if stateDir == "" {
		stateDir = defaultStateDir()
	}

	// Stage 7: save state.json + key file
	t0 = time.Now()
	if svcPlan.System && svcPlan.User != nil {
		if err := ensureNoSymlinkBelowHome(stateDir, svcPlan.User.Home); err != nil {
			return fmt.Errorf("enroll: unsafe state dir: %w", err)
		}
	}
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
		knownDaemons, defaultTarget := buildKnownDaemons(enrollResp)
		bs := &state.BridgeState{
			Version:             state.CurrentVersion,
			DeviceID:            enrollResp.DeviceID,
			AgentName:           enrollResp.Name,
			APIKey:              enrollResp.APIKey,
			Gateway:             state.GatewayRef{URL: gwURL, CertPin: certPinForState(tlsMode, enrollResp.CertPin), TLSMode: tlsModeForState(tlsMode)},
			BridgeStaticKeyPath: keyPath,
			E2EMode:             enrollResp.E2EMode,
			DefaultTarget:       defaultTarget,
			KnownDaemons:        knownDaemons,
			EnrolledAt:          time.Now(),
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
			Gateway:              state.GatewayRef{URL: gwURL, CertPin: certPinForState(tlsMode, enrollResp.CertPin), TLSMode: tlsModeForState(tlsMode)},
			NoiseStaticKeyPath:   keyPath,
			E2EMode:              enrollResp.E2EMode,
			ExecEnabled:          enrollResp.ExecEnabled,
			EnvAllowlist:         enrollResp.EnvAllowlist,
			ForbiddenPorts:       enrollResp.ForbiddenPorts,
			AllowedBridgePubkeys: enrollResp.AllowedBridgePubkeys,
			DefaultWorkingDir:    enrollResp.DefaultWorkingDir,
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

	// For daemons, install a systemd service so it survives logout/reboot
	// (unless the operator opted out). Best-effort: a failure here doesn't
	// fail enroll — we fall back to printing manual start instructions.
	var svc daemonServiceResult
	if role == "daemon" && startService {
		svc = installDaemonService(ctx, stateDir, svcPlan, svcPlanErr)
		outcome.Service = svc.Name
		outcome.ServiceMode = svc.Mode
		outcome.ServiceUser = svc.ServiceUser
		outcome.ServiceOK = svc.OK
		if svc.Err != nil {
			outcome.ServiceError = svc.Err.Error()
		}
	}

	if emitJSON {
		return json.NewEncoder(stdout).Encode(outcome)
	}
	if role == "bridge" {
		fmt.Fprintf(stdout, "✓ Enrolled as bridge %q\n", enrollResp.Name)
		fmt.Fprintf(stdout, "✓ Gateway: %s\n", gwURL)
		fmt.Fprintf(stdout, "✓ Default target: %s\n", enrollResp.TargetDaemon)
		fmt.Fprintf(stdout, "✓ Config written to: %s\n\n", statePath)
		fmt.Fprintln(stdout, "Next: try `agbridge status` to verify the connection.")
		fmt.Fprintln(stdout, "Then `agbridge ai-help` for the AI usage cheat sheet.")
	} else {
		fmt.Fprintf(stdout, "✓ Enrolled as daemon %q\n", enrollResp.Name)
		fmt.Fprintf(stdout, "✓ Gateway: %s\n", gwURL)
		fmt.Fprintf(stdout, "✓ Config written to: %s\n\n", statePath)
		printDaemonServiceResult(stdout, stateDir, svc, startService)
	}
	return nil
}

func certPinForState(tlsMode, pin string) string {
	if tlsMode == auth.GatewayTLSModeWebPKI {
		return ""
	}
	return pin
}

func tlsModeForState(tlsMode string) string {
	if tlsMode == auth.GatewayTLSModePin {
		return ""
	}
	return tlsMode
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

// buildKnownDaemons turns an enroll response into the local KnownDaemons
// pin map plus the default target. A multi-target response populates one
// DaemonRef per Targets entry; an older single-target response (Targets
// empty) falls back to the legacy TargetDaemon/DaemonPubkey pair.
func buildKnownDaemons(resp gateway.EnrollResponse) (map[string]state.DaemonRef, string) {
	out := map[string]state.DaemonRef{}
	if len(resp.Targets) > 0 {
		for _, t := range resp.Targets {
			out[t.Name] = state.DaemonRef{
				Pubkey:    t.DaemonPubkey,
				PinSource: "control-plane",
				FirstSeen: time.Now(),
			}
		}
		return out, resp.Targets[0].Name
	}
	out[resp.TargetDaemon] = state.DaemonRef{
		Pubkey:    resp.DaemonPubkey,
		PinSource: "control-plane",
		FirstSeen: time.Now(),
	}
	return out, resp.TargetDaemon
}
