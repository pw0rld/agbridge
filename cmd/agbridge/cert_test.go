package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/pki"
)

func TestCertGenHumanOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	bin := ensureBinary(t)
	out := t.TempDir()

	cmd := exec.Command(bin, "cert", "gen", "--cn", "gw.example.com", "--days", "30", "--out", out)
	stdout, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cert gen: %v\n%s", err, stdout)
	}
	if !strings.Contains(string(stdout), "pin:  sha256:") {
		t.Errorf("missing pin in stdout: %s", stdout)
	}

	certPath := filepath.Join(out, "cert.pem")
	keyPath := filepath.Join(out, "key.pem")
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("cert.pem missing: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key.pem missing: %v", err)
	}
	certPEM, _ := os.ReadFile(certPath)
	gotPin, err := pki.PinFromCertPEM(certPEM)
	if err != nil {
		t.Fatalf("PinFromCertPEM: %v", err)
	}
	if !strings.Contains(string(stdout), gotPin) {
		t.Errorf("stdout pin doesn't match on-disk pin %q", gotPin)
	}
}

func TestCertGenJSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	bin := ensureBinary(t)
	out := t.TempDir()

	cmd := exec.Command(bin, "cert", "gen", "--cn", "ai.local", "--out", out, "--json")
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("cert gen --json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("json decode: %v: %q", err, stdout)
	}
	for _, k := range []string{"cert", "key", "pin"} {
		if got[k] == "" {
			t.Errorf("missing %q in JSON output: %+v", k, got)
		}
	}
	if !strings.HasPrefix(got["pin"], "sha256:") {
		t.Errorf("pin format: %q", got["pin"])
	}
	// Verify the pin matches the actual on-disk cert.
	certPEM, _ := os.ReadFile(got["cert"])
	expected, _ := pki.PinFromCertPEM(certPEM)
	if got["pin"] != expected {
		t.Errorf("pin mismatch: cli %q vs computed %q", got["pin"], expected)
	}
}
