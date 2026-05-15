package main

// End-to-end tests against the compiled agbridge binary. Each test compiles
// the binary once (sync.Once), generates a fresh self-signed TLS cert,
// renders YAML configs into a temp dir, and spawns real gateway/daemon/
// bridge processes. The bridge's stdin/stdout speaks MCP JSON-RPC.
//
// These complement smoke_test.go (which is in-process and bypasses cobra/
// TLS/signal handling) by exercising the actual binary surface: CLI parse,
// TLS PEM load, YAML validation, SIGTERM/SIGHUP, stdio buffering.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	b64 "encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/auth"
)

// -------- binary build (once per `go test` invocation) --------

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// ensureBinary builds the agbridge binary into a temp dir and returns its
// path. Subsequent calls return the cached path.
func ensureBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agbridge-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "agbridge")
		cmd := exec.Command("go", "build", "-o", binPath, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// -------- TLS cert + pin --------

type tlsBundle struct {
	certPath string
	keyPath  string
	pin      string // "sha256:<hex>"
}

func generateTLSBundle(t *testing.T, dir string) tlsBundle {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "agbridge-bin-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshalkey: %v", err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	sum := sha256.Sum256(der)
	return tlsBundle{certPath: certPath, keyPath: keyPath, pin: "sha256:" + hex.EncodeToString(sum[:])}
}

// -------- process helpers --------

// runningProc bundles a child process with its piped streams. stderr is
// mu-protected because the stderr-draining goroutine and test assertions
// can both touch it.
type runningProc struct {
	cmd      *exec.Cmd
	name     string
	stderrMu sync.Mutex
	stderr   *strings.Builder
	stdin    io.WriteCloser // bridge only
	stdout   io.ReadCloser  // bridge only
}

func (rp *runningProc) appendStderr(line string) {
	rp.stderrMu.Lock()
	rp.stderr.WriteString(line + "\n")
	rp.stderrMu.Unlock()
}

func (rp *runningProc) stderrSnapshot() string {
	rp.stderrMu.Lock()
	defer rp.stderrMu.Unlock()
	return rp.stderr.String()
}

// waitForStderr blocks until a stderr line matching re appears or timeout
// expires. Returns the matched line on success, fatal on timeout.
func (rp *runningProc) waitForStderr(t *testing.T, re *regexp.Regexp, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m := re.FindString(rp.stderrSnapshot()); m != "" {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: never saw %q within %v; stderr:\n%s", rp.name, re.String(), timeout, rp.stderrSnapshot())
	return ""
}

// startGateway spawns the gateway binary and blocks until it logs
// "listening on" or readyTimeout elapses. Returns the parsed address.
func (h *BinaryHarness) startGateway(t *testing.T) (*runningProc, string) {
	t.Helper()
	cmd := exec.Command(h.binPath, "gateway", "--config", h.gatewayCfgPath, "--cert", h.tls.certPath, "--key", h.tls.keyPath)
	rp := &runningProc{cmd: cmd, name: "gateway", stderr: &strings.Builder{}}
	addr := startProcReady(t, rp, regexp.MustCompile(`listening on (127\.0\.0\.1:\d+)`), 5*time.Second)
	t.Cleanup(func() { _ = killProc(rp) })
	return rp, "wss://" + addr + "/"
}

// startDaemon spawns the daemon binary and blocks until handshake-ok log.
// When the test process is root, AGBRIDGE_TEST_ALLOW_ROOT=1 is propagated
// to bypass sandbox.RefuseRoot — see sandbox.RefuseRoot doc for why.
func (h *BinaryHarness) startDaemon(t *testing.T) *runningProc {
	t.Helper()
	cmd := exec.Command(h.binPath, "daemon", "--config", h.daemonCfgPath)
	if os.Geteuid() == 0 {
		cmd.Env = append(os.Environ(), "AGBRIDGE_TEST_ALLOW_ROOT=1")
	}
	rp := &runningProc{cmd: cmd, name: "daemon", stderr: &strings.Builder{}}
	startProcReady(t, rp, regexp.MustCompile(`daemon: handshake ok`), 5*time.Second)
	t.Cleanup(func() { _ = killProc(rp) })
	return rp
}

// startBridge spawns the bridge binary with stdio pipes wired so the test
// can drive JSON-RPC. Blocks until handshake-ok log.
func (h *BinaryHarness) startBridge(t *testing.T) *runningProc {
	t.Helper()
	cmd := exec.Command(h.binPath, "bridge", "--config", h.bridgeCfgPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	rp := &runningProc{cmd: cmd, name: "bridge", stderr: &strings.Builder{}, stdin: stdin, stdout: stdout}
	startProcReady(t, rp, regexp.MustCompile(`bridge: handshake ok`), 5*time.Second)
	t.Cleanup(func() { _ = killProc(rp) })
	return rp
}

// startProcReady starts rp.cmd, drains stderr into a goroutine, and blocks
// until readyRe matches a line or timeout. Returns the first capture group
// (or full match if no group).
func startProcReady(t *testing.T, rp *runningProc, readyRe *regexp.Regexp, timeout time.Duration) string {
	t.Helper()
	stderrPipe, err := rp.cmd.StderrPipe()
	if err != nil {
		t.Fatalf("%s stderr pipe: %v", rp.name, err)
	}
	if err := rp.cmd.Start(); err != nil {
		t.Fatalf("%s start: %v", rp.name, err)
	}

	readyCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderrPipe)
		matched := false
		for sc.Scan() {
			line := sc.Text()
			rp.appendStderr(line)
			t.Logf("[%s] %s", rp.name, line)
			if !matched {
				if m := readyRe.FindStringSubmatch(line); m != nil {
					if len(m) > 1 {
						readyCh <- m[1]
					} else {
						readyCh <- m[0]
					}
					matched = true
				}
			}
		}
		if !matched {
			close(readyCh)
		}
	}()

	select {
	case m, ok := <-readyCh:
		if !ok {
			t.Fatalf("%s exited before ready; stderr:\n%s", rp.name, rp.stderrSnapshot())
		}
		return m
	case <-time.After(timeout):
		_ = killProc(rp)
		t.Fatalf("%s ready timeout; stderr:\n%s", rp.name, rp.stderrSnapshot())
	}
	return ""
}

// killProc sends SIGTERM and waits up to 2s for the process to exit; falls
// back to SIGKILL.
func killProc(rp *runningProc) error {
	if rp == nil || rp.cmd == nil || rp.cmd.Process == nil {
		return nil
	}
	_ = rp.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- rp.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		_ = rp.cmd.Process.Kill()
		return <-done
	}
}

// -------- harness --------

type BinaryHarness struct {
	tmpDir         string
	binPath        string
	tls            tlsBundle
	gatewayURL     string
	gatewayCfgPath string
	daemonCfgPath  string
	bridgeCfgPath  string
	apiKey         string
	daemonTok      string
	scratchDir     string // daemon-allowed paths for read_file/write_file/exec
}

// newBinaryHarness sets up temp dirs, generates TLS material, and writes
// the gateway YAML. The daemon/bridge YAMLs are written by writeAllConfigs
// after we know the gateway's actual listen address (since Listen is 0 by
// default).
func newBinaryHarness(t *testing.T) *BinaryHarness {
	t.Helper()
	tmp := t.TempDir()
	scratch := filepath.Join(tmp, "scratch")
	if err := os.Mkdir(scratch, 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	h := &BinaryHarness{
		tmpDir:     tmp,
		binPath:    ensureBinary(t),
		apiKey:     "test-api-key",
		daemonTok:  "test-daemon-token",
		scratchDir: scratch,
	}
	h.tls = generateTLSBundle(t, tmp)
	return h
}

// writeGatewayCfg renders gateway.yaml. Listen=127.0.0.1:0 lets the OS
// pick a port — startGateway parses it from the "listening on" log line.
func (h *BinaryHarness) writeGatewayCfg(t *testing.T) {
	t.Helper()
	h.gatewayCfgPath = filepath.Join(h.tmpDir, "gateway.yaml")
	body := fmt.Sprintf(`listen: 127.0.0.1:0
audit_path: %s/audit.jsonl
agents:
  - name: claude-laptop
    api_key_hash: sha256:%s
    allowed_daemons: [lab01]
daemons:
  - name: lab01
    token_hash: sha256:%s
`, h.tmpDir, auth.SHA256Hex([]byte(h.apiKey)), auth.SHA256Hex([]byte(h.daemonTok)))
	if err := os.WriteFile(h.gatewayCfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write gateway cfg: %v", err)
	}
}

// writeDaemonCfg renders daemon.yaml. Requires h.gatewayURL to be set
// (i.e., after startGateway).
func (h *BinaryHarness) writeDaemonCfg(t *testing.T) {
	t.Helper()
	h.daemonCfgPath = filepath.Join(h.tmpDir, "daemon.yaml")
	body := fmt.Sprintf(`gateway_url: %s
daemon_name: lab01
registration_token: %s
cert_pin: %s
allowed_exec_cwds:
  - %s
  - %s/*
allowed_read_paths:
  - %s
  - %s/*
allowed_write_paths:
  - %s
  - %s/*
env_allowlist:
  - PATH
`, h.gatewayURL, h.daemonTok, h.tls.pin,
		h.scratchDir, h.scratchDir,
		h.scratchDir, h.scratchDir,
		h.scratchDir, h.scratchDir)
	if err := os.WriteFile(h.daemonCfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write daemon cfg: %v", err)
	}
}

func (h *BinaryHarness) writeBridgeCfg(t *testing.T) {
	t.Helper()
	h.bridgeCfgPath = filepath.Join(h.tmpDir, "bridge.yaml")
	body := fmt.Sprintf(`gateway_url: %s
agent_name: claude-laptop
api_key: %s
cert_pin: %s
target_daemon: lab01
`, h.gatewayURL, h.apiKey, h.tls.pin)
	if err := os.WriteFile(h.bridgeCfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write bridge cfg: %v", err)
	}
}

// -------- MCP JSON-RPC over the bridge's stdio --------

// rpcCall sends a JSON-RPC request line, reads the next stdout line, parses
// as a JSON-RPC response. Caller is responsible for managing request ids
// (each call is independent: a single response is expected).
func (h *BinaryHarness) rpcCall(t *testing.T, bridge *runningProc, id int, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	enc, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode rpc: %v", err)
	}
	if _, err := bridge.stdin.Write(append(enc, '\n')); err != nil {
		t.Fatalf("rpc write: %v", err)
	}

	type readResult struct {
		line string
		err  error
	}
	resCh := make(chan readResult, 1)
	go func() {
		br := bufio.NewReader(bridge.stdout)
		line, err := br.ReadString('\n')
		resCh <- readResult{line, err}
	}()

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("rpc read: %v (stderr:\n%s)", r.err, bridge.stderrSnapshot())
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(r.line)), &resp); err != nil {
			t.Fatalf("rpc decode: %v: %q", err, r.line)
		}
		return resp
	case <-time.After(10 * time.Second):
		t.Fatalf("rpc timeout; stderr:\n%s", bridge.stderrSnapshot())
	}
	return nil
}

// initializeBridge sends MCP initialize once per bridge session. Must be
// called before any callTool.
func (h *BinaryHarness) initializeBridge(t *testing.T, bridge *runningProc) {
	t.Helper()
	resp := h.rpcCall(t, bridge, 1, "initialize", map[string]any{})
	if resp["error"] != nil {
		t.Fatalf("initialize error: %+v", resp)
	}
}

// callTool sends one tools/call and returns the result map. id must be
// unique per call within a session.
func (h *BinaryHarness) callTool(t *testing.T, bridge *runningProc, id int, name string, args map[string]any) map[string]any {
	t.Helper()
	resp := h.rpcCall(t, bridge, id, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result for %s: %+v", name, resp)
	}
	return res
}

// setupRunning is the standard sequence: write gateway cfg, start gateway,
// learn URL, write daemon+bridge cfgs, start daemon+bridge.
func (h *BinaryHarness) setupRunning(t *testing.T) (*runningProc, *runningProc, *runningProc) {
	t.Helper()
	h.writeGatewayCfg(t)
	gw, url := h.startGateway(t)
	h.gatewayURL = url
	h.writeDaemonCfg(t)
	h.writeBridgeCfg(t)
	dm := h.startDaemon(t)
	br := h.startBridge(t)
	return gw, dm, br
}

// silence unused import warning from context (used in future tests)
var _ = context.Background

// ============================================================================
// Tests
// ============================================================================

// TestBinaryFourToolsEndToEnd spawns real gateway/daemon/bridge processes
// and drives all four MCP tools via the bridge's stdin/stdout JSON-RPC.
// This is the only test that validates the real binary surface (cobra,
// TLS PEM, YAML config, stdio).
func TestBinaryFourToolsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	_, _, bridge := h.setupRunning(t)
	h.initializeBridge(t, bridge)

	// 1. exec
	res := h.callTool(t, bridge, 2, "exec", map[string]any{
		"cmd":  "/bin/echo",
		"args": []any{"phase-B-binary"},
		"cwd":  h.scratchDir,
	})
	meta := res["_meta"].(map[string]any)
	if int(meta["exitcode"].(float64)) != 0 {
		t.Fatalf("exec exitcode: %v (full meta %+v)", meta["exitcode"], meta)
	}
	stdoutB64 := meta["stdout_b64"].(string)
	stdout, _ := base64DecodeString(stdoutB64)
	if !strings.Contains(stdout, "phase-B-binary") {
		t.Errorf("exec stdout missing marker, got %q", stdout)
	}

	// 2. write_file
	wantBytes := []byte("phase B write content\n")
	res = h.callTool(t, bridge, 3, "write_file", map[string]any{
		"path":        filepath.Join(h.scratchDir, "out.txt"),
		"content_b64": base64EncodeBytes(wantBytes),
		"mode":        0o644,
	})
	meta = res["_meta"].(map[string]any)
	if int(meta["bytes_written"].(float64)) != len(wantBytes) {
		t.Fatalf("write_file bytes_written: %v want %d", meta["bytes_written"], len(wantBytes))
	}

	// 3. read_file (round-trip the file we just wrote)
	res = h.callTool(t, bridge, 4, "read_file", map[string]any{
		"path": filepath.Join(h.scratchDir, "out.txt"),
	})
	meta = res["_meta"].(map[string]any)
	gotB64 := meta["content_b64"].(string)
	got, _ := base64DecodeBytes(gotB64)
	if string(got) != string(wantBytes) {
		t.Errorf("read_file content mismatch: got %q want %q", got, wantBytes)
	}

	// 4. port_forward (in-test echo server, daemon dials it through the
	// loopback)
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	echoPort := echoLn.Addr().(*net.TCPAddr).Port

	res = h.callTool(t, bridge, 5, "port_forward", map[string]any{
		"remote_host": "127.0.0.1",
		"remote_port": echoPort,
	})
	meta = res["_meta"].(map[string]any)
	localPort := int(meta["local_port"].(float64))
	if localPort == 0 {
		t.Fatalf("port_forward local_port missing: %+v", meta)
	}

	// Dial the bridge-side listener and echo a payload back.
	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		conn, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial port_forward local: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := []byte("hello phase-B\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("pf write: %v", err)
	}
	gotBuf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, gotBuf); err != nil {
		t.Fatalf("pf read: %v", err)
	}
	if string(gotBuf) != string(msg) {
		t.Errorf("pf echo mismatch: got %q want %q", gotBuf, msg)
	}
}

// base64 helpers used by the test bodies. Kept inline so the helpers list
// at the top stays focused on harness machinery.
func base64DecodeString(s string) (string, error) {
	b, err := base64DecodeBytes(s)
	return string(b), err
}
func base64DecodeBytes(s string) ([]byte, error) {
	return b64.StdEncoding.DecodeString(s)
}
func base64EncodeBytes(b []byte) string {
	return b64.StdEncoding.EncodeToString(b)
}

// TestBinarySIGHUPRevoke verifies that SIGHUP on the gateway re-reads its
// YAML, swaps the credential registry, and closes sessions whose principal
// was removed — through the real binary signal path (not the in-process
// shim that Phase 5 unit tests use).
func TestBinarySIGHUPRevoke(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	gw, _, bridge := h.setupRunning(t)
	h.initializeBridge(t, bridge)

	// Baseline: bridge can exec through the gateway.
	res := h.callTool(t, bridge, 2, "exec", map[string]any{
		"cmd":  "/bin/echo",
		"args": []any{"before-revoke"},
		"cwd":  h.scratchDir,
	})
	if int(res["_meta"].(map[string]any)["exitcode"].(float64)) != 0 {
		t.Fatalf("pre-revoke exec failed: %+v", res["_meta"])
	}

	// Rewrite gateway.yaml with claude-laptop removed and SIGHUP.
	revokedYAML := fmt.Sprintf(`listen: 127.0.0.1:0
audit_path: %s/audit.jsonl
agents: []
daemons:
  - name: lab01
    token_hash: sha256:%s
`, h.tmpDir, auth.SHA256Hex([]byte(h.daemonTok)))
	if err := os.WriteFile(h.gatewayCfgPath, []byte(revokedYAML), 0o600); err != nil {
		t.Fatalf("rewrite gateway cfg: %v", err)
	}
	if err := gw.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("signal SIGHUP: %v", err)
	}

	// Gateway logs the revocation; assert it explicitly.
	gw.waitForStderr(t, regexp.MustCompile(`SIGHUP: reloaded; revoked 1 sessions: \[bridge/claude-laptop\]`), 3*time.Second)

	// Next call on the bridge must error: gateway closed the wss conn, so
	// the bridge's runReader exited and the supervisor is in dialer.Loop
	// trying to re-handshake (which will keep failing with auth_failed).
	// In-flight tools/call sees ch closed → network_lost.
	resp := h.rpcCall(t, bridge, 3, "tools/call", map[string]any{
		"name": "exec",
		"arguments": map[string]any{
			"cmd":  "/bin/echo",
			"args": []any{"after-revoke"},
			"cwd":  h.scratchDir,
		},
	})
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("post-revoke response had no result: %+v", resp)
	}
	if result["isError"] != true {
		t.Errorf("post-revoke exec should return isError=true, got %+v", result)
	}
	meta := result["_meta"].(map[string]any)
	if meta["error_code"] != "network_lost" {
		t.Errorf("post-revoke error_code: got %v want network_lost (meta: %+v)", meta["error_code"], meta)
	}
}

// TestBinaryCleanShutdownGateway sends SIGTERM to the gateway process and
// asserts it exits cleanly (exit code 0) within 2 seconds.
func TestBinaryCleanShutdownGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	h.writeGatewayCfg(t)
	gw, _ := h.startGateway(t)

	if err := gw.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	if err := waitExit(gw, 2*time.Second); err != nil {
		t.Errorf("gateway shutdown: %v; stderr:\n%s", err, gw.stderrSnapshot())
	}
}

// TestBinaryCleanShutdownDaemon: SIGTERM the daemon, assert clean exit.
func TestBinaryCleanShutdownDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	_, daemon, _ := h.setupRunning(t)
	if err := daemon.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	if err := waitExit(daemon, 2*time.Second); err != nil {
		t.Errorf("daemon shutdown: %v; stderr:\n%s", err, daemon.stderrSnapshot())
	}
}

// TestBinaryCleanShutdownBridge: SIGTERM the bridge, assert clean exit.
// Bridge currently does NOT install signal.NotifyContext (RunE uses plain
// context.WithCancel). If this test fails with "process killed", that's
// the bug — fix is to switch bridge RunE to signal.NotifyContext too.
func TestBinaryCleanShutdownBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	_, _, bridge := h.setupRunning(t)
	if err := bridge.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}
	if err := waitExit(bridge, 2*time.Second); err != nil {
		t.Errorf("bridge shutdown: %v; stderr:\n%s", err, bridge.stderrSnapshot())
	}
}

// waitExit blocks until rp.cmd exits or timeout. Returns nil if exit was
// clean (code 0); error otherwise (timeout, non-zero exit, signal).
func waitExit(rp *runningProc, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- rp.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		return fmt.Errorf("non-zero exit: %v", err)
	case <-time.After(timeout):
		_ = rp.cmd.Process.Kill()
		<-done
		return fmt.Errorf("did not exit within %v", timeout)
	}
}
