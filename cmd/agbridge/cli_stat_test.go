package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

// TestStatLiveAgainstFixture seeds a file, stats it through the CLI
// wrapper, and verifies the daemon-reported type + size are echoed.
func TestStatLiveAgainstFixture(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "data.bin")
	body := []byte("0123456789abcdef") // 16 bytes
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	env := setupPhase4(t, &config.DaemonConfig{
		DefaultWorkingDir: tmpDir,
	})
	opts := env.newCLIOpts()
	g.timeout = 5 * time.Second
	defer func() { g.timeout = 0 }()

	var out bytes.Buffer
	cmd := newStatCmd()
	cmd.SetOut(&out)
	cmd.SetContext(env.ctx)
	if err := runStat(cmd, path, opts); err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "type:     file") {
		t.Errorf("expected type=file, got:\n%s", got)
	}
	if !strings.Contains(got, fmt.Sprintf("size:     %d", len(body))) {
		t.Errorf("expected size=%d, got:\n%s", len(body), got)
	}
}
