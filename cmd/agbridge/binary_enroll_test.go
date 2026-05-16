package main

// Integration test for the Phase B enroll flow:
//   1. Start gateway with public_url set to its bound URL.
//   2. agbridge issue-token --role=daemon ...           → token D
//   3. agbridge enroll  --token=D  --state-dir=daemonDir → writes state.json
//   4. Start daemon with --state-dir=daemonDir
//   5. agbridge issue-token --role=bridge --target=...   → token B
//   6. agbridge enroll  --token=B  --state-dir=bridgeDir → writes state.json
//   7. Start bridge with --state-dir=bridgeDir
//   8. exec /bin/echo via MCP, verify stdout.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBinaryEnrollFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("binary enroll: skipping in -short mode")
	}
	if os.Getuid() == 0 {
		os.Setenv("AGBRIDGE_TEST_ALLOW_ROOT", "1")
	}

	h := newBinaryHarness(t)

	// Pre-allocate a TCP port so we can pin public_url before starting.
	port := pickFreePort(t)
	gwURL := fmt.Sprintf("wss://127.0.0.1:%d/", port)

	cfgPath := filepath.Join(h.tmpDir, "gateway.yaml")
	body := fmt.Sprintf(`listen: 127.0.0.1:%d
public_url: %s
audit_path: %s/audit.jsonl
agents: []
daemons: []
`, port, gwURL, h.tmpDir)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write gw yaml: %v", err)
	}

	gw := startGatewayPort(t, h.binPath, cfgPath, h.tls.certPath, h.tls.keyPath, port)
	defer killProc(gw)

	// 1. Daemon enroll
	daemonStateDir := filepath.Join(h.tmpDir, "daemon-state")
	daemonTok := issueTokenJSON(t, h.binPath, cfgPath, []string{
		"--role=daemon", "--name=lab01",
		"--allowed-paths=" + h.scratchDir,
	})
	runEnrollBinary(t, h.binPath, gwURL, daemonTok, daemonStateDir)
	assertDaemonStateValid(t, filepath.Join(daemonStateDir, "state.json"), h.scratchDir)

	dm := startDaemonFromState(t, h.binPath, daemonStateDir)
	defer killProc(dm)

	// 2. Bridge enroll (requires daemon registered)
	bridgeStateDir := filepath.Join(h.tmpDir, "bridge-state")
	bridgeTok := issueTokenJSON(t, h.binPath, cfgPath, []string{
		"--role=bridge", "--name=laptop", "--target=lab01",
	})
	runEnrollBinary(t, h.binPath, gwURL, bridgeTok, bridgeStateDir)

	br := startBridgeFromState(t, h.binPath, bridgeStateDir)
	defer killProc(br)

	// 3. Round-trip exec via MCP
	h.initializeBridge(t, br)
	res := h.callTool(t, br, 2, "exec", map[string]any{
		"cmd":  "/bin/echo",
		"args": []any{"phase-B-enroll-works"},
		"cwd":  h.scratchDir,
	})
	meta := res["_meta"].(map[string]any)
	if int(meta["exitcode"].(float64)) != 0 {
		t.Fatalf("exec exitcode: %+v", meta)
	}
	stdoutB64 := meta["stdout_b64"].(string)
	stdout, err := base64DecodeString(stdoutB64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "phase-B-enroll-works") {
		t.Fatalf("exec stdout missing marker: %q", stdout)
	}
}

// pickFreePort opens then closes a TCP listener to learn a free port.
// Tiny race window between close and reuse — fine for test.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("alloc port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startGatewayPort starts gateway and waits for the "listening on 127.0.0.1:<port>"
// log line confirming it bound the expected port.
func startGatewayPort(t *testing.T, bin, cfg, cert, key string, port int) *runningProc {
	t.Helper()
	cmd := exec.Command(bin, "gateway", "--config", cfg, "--cert", cert, "--key", key)
	rp := &runningProc{cmd: cmd, name: "gateway", stderr: &strings.Builder{}}
	pat := fmt.Sprintf(`gateway listening on 127\.0\.0\.1:%d`, port)
	startProcReady(t, rp, regexp.MustCompile(pat), 5*time.Second)
	return rp
}

func startDaemonFromState(t *testing.T, bin, stateDir string) *runningProc {
	t.Helper()
	cmd := exec.Command(bin, "daemon", "--state-dir", stateDir)
	rp := &runningProc{cmd: cmd, name: "daemon", stderr: &strings.Builder{}}
	startProcReady(t, rp, regexp.MustCompile(`daemon: handshake ok`), 5*time.Second)
	return rp
}

func startBridgeFromState(t *testing.T, bin, stateDir string) *runningProc {
	t.Helper()
	cmd := exec.Command(bin, "bridge", "--state-dir", stateDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("bridge stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("bridge stdout pipe: %v", err)
	}
	rp := &runningProc{cmd: cmd, name: "bridge", stderr: &strings.Builder{}, stdin: stdin, stdout: stdout}
	startProcReady(t, rp, regexp.MustCompile(`bridge: handshake ok`), 5*time.Second)
	return rp
}

func issueTokenJSON(t *testing.T, bin, gwCfg string, extra []string) string {
	t.Helper()
	args := append([]string{"issue-token", "--config", gwCfg, "--json"}, extra...)
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		t.Fatalf("issue-token %v: %v (%s)", extra, err, out)
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("parse issue-token JSON: %v (%s)", err, out)
	}
	if resp.Token == "" {
		t.Fatalf("empty token: %s", out)
	}
	return resp.Token
}

func runEnrollBinary(t *testing.T, bin, gwURL, token, stateDir string) {
	t.Helper()
	cmd := exec.Command(bin, "enroll", "--gateway", gwURL, "--token", token, "--state-dir", stateDir, "--json")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("enroll failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	var outcome struct {
		OK    bool   `json:"ok"`
		Stage string `json:"stage,omitempty"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &outcome); err != nil {
		t.Fatalf("parse enroll outcome: %v\nstdout: %s", err, stdout.String())
	}
	if !outcome.OK {
		t.Fatalf("enroll outcome not ok: stage=%s err=%s", outcome.Stage, outcome.Error)
	}
}

func assertDaemonStateValid(t *testing.T, path, expectedAllowedPath string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse state.json: %v", err)
	}
	if doc["daemon_name"] != "lab01" {
		t.Fatalf("daemon_name wrong: %v", doc["daemon_name"])
	}
	cwds, _ := doc["allowed_exec_cwds"].([]any)
	hit := false
	for _, p := range cwds {
		if p == expectedAllowedPath {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatalf("allowed_exec_cwds did not include %q: %v", expectedAllowedPath, cwds)
	}
}
