package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/state"
)

func TestGlobalFlagsRegistered(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--help"})
	_ = root.Execute()
	out := buf.String()
	for _, want := range []string{"--target", "--json", "--state-dir", "--timeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help missing flag %q\noutput: %s", want, out)
		}
	}
}

func TestLoadOptsAppliesTargetOverride(t *testing.T) {
	dir := t.TempDir()
	st := &state.BridgeState{
		Version:       state.CurrentVersion,
		AgentName:     "alice",
		APIKey:        "k",
		Gateway:       state.GatewayRef{URL: "wss://gw/", TLSMode: "pin", CertPin: "sha256:" + strings.Repeat("a", 64)},
		E2EMode:       "disabled",
		DefaultTarget: "dev1",
		KnownDaemons: map[string]state.DaemonRef{
			"dev1": {DeviceID: "d1", Pubkey: "p1", PinSource: "tofu", FirstSeen: time.Now()},
			"prod": {DeviceID: "d2", Pubkey: "p2", PinSource: "tofu", FirstSeen: time.Now()},
		},
	}
	if err := state.SaveBridge(filepath.Join(dir, "state.json"), st); err != nil {
		t.Fatal(err)
	}
	g.stateDir = dir
	g.target = ""
	opts, err := loadOpts()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Target != "dev1" {
		t.Errorf("default target: got %q want dev1", opts.Target)
	}
	g.target = "prod"
	opts, err = loadOpts()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Target != "prod" {
		t.Errorf("override target: got %q want prod", opts.Target)
	}
	if opts.DaemonPubkey != "p2" {
		t.Errorf("override pubkey: got %q want p2", opts.DaemonPubkey)
	}
	g.target = "nonexistent"
	if _, err = loadOpts(); err == nil {
		t.Error("expected unknown_target error")
	} else if !strings.Contains(err.Error(), "unknown_target") {
		t.Errorf("unexpected error: %v", err)
	}
	g.target = ""
	g.stateDir = ""
}
