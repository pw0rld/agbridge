package main

import (
	"os"
	"testing"

	"github.com/pw0rld/agbridge/internal/config"
)

func TestEffectiveDefaultWorkingDir(t *testing.T) {
	// configured value wins
	if got := effectiveDefaultWorkingDir(&config.DaemonConfig{DefaultWorkingDir: "/srv/work"}); got != "/srv/work" {
		t.Fatalf("configured: got %q, want /srv/work", got)
	}
	// empty → daemon user home
	home, _ := os.UserHomeDir()
	if got := effectiveDefaultWorkingDir(&config.DaemonConfig{}); got != home {
		t.Fatalf("fallback: got %q, want %q", got, home)
	}
}
