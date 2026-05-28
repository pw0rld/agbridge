package main

// Shared helpers for binary tests that spawn the real agbridge binary.
// v0.0.1 had a much larger binary harness wired for MCP-stdio bridge tests;
// v0.0.2 deletes that surface, leaving only this minimal `ensureBinary`
// for tests that exercise standalone subcommands (cert gen, keygen).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// ensureBinary builds the agbridge binary once per `go test` invocation
// and returns the absolute path. Subsequent calls reuse the same binary.
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
