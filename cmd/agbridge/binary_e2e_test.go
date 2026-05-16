package main

// E2E integration tests. Same binary harness as binary_test.go, but with
// e2e_mode=required enabled. Verifies:
//   1. Happy path — Noise IK handshake + exec round-trip through AEAD-
//      wrapped Route frames.
//   2. ACL rejection — daemon's allowed_bridge_pubkeys mismatching the
//      bridge's actual pubkey causes the bridge to fail its initial
//      handshake (non-zero exit).

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/e2e"
)

// TestBinaryE2EHappyPath spawns gateway+daemon+bridge with e2e_mode=required
// and verifies a simple exec round-trip works (which validates: Noise IK
// handshake, AEAD wrap/unwrap, ACL passes).
func TestBinaryE2EHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("binary E2E test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	h.enableE2E(t)

	_, _, bridge := h.setupRunning(t)
	h.initializeBridge(t, bridge)

	res := h.callTool(t, bridge, 2, "exec", map[string]any{
		"cmd":  "/bin/echo",
		"args": []any{"e2e-happy-path"},
		"cwd":  h.scratchDir,
	})
	meta := res["_meta"].(map[string]any)
	if int(meta["exitcode"].(float64)) != 0 {
		t.Fatalf("exec exitcode: %v (full meta %+v)", meta["exitcode"], meta)
	}
	stdoutB64 := meta["stdout_b64"].(string)
	stdout, err := base64DecodeString(stdoutB64)
	if err != nil {
		t.Fatalf("decode stdout_b64: %v", err)
	}
	if !strings.Contains(stdout, "e2e-happy-path") {
		t.Errorf("exec stdout missing E2E marker, got %q", stdout)
	}
}

// TestBinaryE2EACLRejection sets the daemon's allowed_bridge_pubkeys to a
// random key (not the bridge's actual pubkey). The bridge's initial Noise
// handshake should fail; the process must exit non-zero with stderr
// indicating handshake failure.
func TestBinaryE2EACLRejection(t *testing.T) {
	if testing.Short() {
		t.Skip("binary E2E test: skipping in -short mode")
	}
	h := newBinaryHarness(t)
	h.enableE2E(t)

	// Replace the legitimate bridge pubkey with a fresh unrelated one.
	stranger, err := e2e.GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	h.allowedBridgePubs = []string{stranger.PubBase64()}

	h.writeGatewayCfg(t)
	gw, url := h.startGateway(t)
	_ = gw
	h.gatewayURL = url
	h.writeDaemonCfg(t)
	h.writeBridgeCfg(t)
	dm := h.startDaemon(t)
	_ = dm

	// Spawn bridge directly (not via startBridge which expects success).
	cmd := exec.Command(h.binPath, "bridge", "--config", h.bridgeCfgPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("") // closed stdin → MCP server exits cleanly
	if err := cmd.Start(); err != nil {
		t.Fatalf("bridge start: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case waitErr := <-done:
		if waitErr == nil {
			t.Fatalf("bridge unexpectedly exited 0; stderr:\n%s", stderr.String())
		}
		log := stderr.String()
		// Bridge prints "initial handshake: noise handshake: daemon rejected
		// handshake: handshake_failed" when daemon's ACL refuses our pubkey.
		if !strings.Contains(log, "noise handshake") && !strings.Contains(log, "handshake_failed") {
			t.Fatalf("bridge stderr missing expected handshake-failure markers; got:\n%s", log)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("bridge did not exit within 5s of ACL-rejected handshake; stderr:\n%s", stderr.String())
	}
}
