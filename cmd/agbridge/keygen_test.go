package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/auth"
)

func TestKeygenHumanOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	bin := ensureBinary(t)
	out, err := exec.Command(bin, "keygen").Output()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "secret: ") {
		t.Errorf("line 0 missing secret: prefix: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "hash:   sha256:") {
		t.Errorf("line 1 missing hash: sha256: prefix: %q", lines[1])
	}
}

func TestKeygenJSONOutputCrossChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	bin := ensureBinary(t)
	out, err := exec.Command(bin, "keygen", "--json").Output()
	if err != nil {
		t.Fatalf("keygen --json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json decode: %v: %q", err, out)
	}
	secret := got["secret"]
	hash := got["hash"]
	if secret == "" || !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("malformed output: %+v", got)
	}
	expected := "sha256:" + auth.SHA256Hex([]byte(secret))
	if hash != expected {
		t.Errorf("hash != sha256(secret): got %q, recomputed %q", hash, expected)
	}
}

func TestKeygenProducesUniqueSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test: skipping in -short mode")
	}
	bin := ensureBinary(t)
	seen := map[string]struct{}{}
	for i := 0; i < 5; i++ {
		out, err := exec.Command(bin, "keygen", "--json").Output()
		if err != nil {
			t.Fatalf("keygen: %v", err)
		}
		var got map[string]string
		_ = json.Unmarshal(out, &got)
		if _, dup := seen[got["secret"]]; dup {
			t.Fatalf("duplicate secret across runs: %q", got["secret"])
		}
		seen[got["secret"]] = struct{}{}
	}
}
