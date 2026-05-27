package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/state"
)

func TestTargetsLists(t *testing.T) {
	dir := t.TempDir()
	st := &state.BridgeState{
		Version:       state.CurrentVersion,
		AgentName:     "alice",
		DefaultTarget: "dev1",
		Gateway:       state.GatewayRef{URL: "wss://gw/", TLSMode: "pin"},
		E2EMode:       "disabled",
		KnownDaemons: map[string]state.DaemonRef{
			"dev1": {DeviceID: "d1", Pubkey: "p1aaaaaaaaaaaaaaaaaaaaaaaaaaaa", PinSource: "tofu", FirstSeen: time.Now()},
			"prod": {DeviceID: "d2", Pubkey: "p2bbbbbbbbbbbbbbbbbbbbbbbbbbbb", PinSource: "tofu", FirstSeen: time.Now()},
		},
	}
	if err := state.SaveBridge(filepath.Join(dir, "state.json"), st); err != nil {
		t.Fatal(err)
	}
	g.stateDir = dir
	defer func() { g.stateDir = "" }()

	var out bytes.Buffer
	cmd := newTargetsCmd()
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	for _, want := range []string{"dev1", "prod", "*"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestTargetsJSON(t *testing.T) {
	dir := t.TempDir()
	st := &state.BridgeState{
		Version:       state.CurrentVersion,
		DefaultTarget: "dev1",
		KnownDaemons: map[string]state.DaemonRef{
			"dev1": {DeviceID: "d1", Pubkey: "p1", PinSource: "tofu", FirstSeen: time.Now()},
		},
	}
	_ = state.SaveBridge(filepath.Join(dir, "state.json"), st)
	g.stateDir = dir
	g.json = true
	defer func() { g.stateDir = ""; g.json = false }()

	var out bytes.Buffer
	cmd := newTargetsCmd()
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`"_schema":1`, `"dev1"`, `"default":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("JSON missing %q:\n%s", want, got)
		}
	}
}
