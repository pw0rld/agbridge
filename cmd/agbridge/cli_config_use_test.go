package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/state"
)

func TestConfigUseSetsDefault(t *testing.T) {
	dir := t.TempDir()
	st := &state.BridgeState{
		Version:       state.CurrentVersion,
		DefaultTarget: "dev1",
		KnownDaemons: map[string]state.DaemonRef{
			"dev1": {DeviceID: "d1", Pubkey: "p1"},
			"prod": {DeviceID: "d2", Pubkey: "p2"},
		},
	}
	_ = state.SaveBridge(filepath.Join(dir, "state.json"), st)
	g.stateDir = dir
	defer func() { g.stateDir = "" }()

	cmd := newConfigUseCmd()
	if err := cmd.RunE(cmd, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	got, _ := state.LoadBridge(filepath.Join(dir, "state.json"))
	if got.DefaultTarget != "prod" {
		t.Errorf("default still %q", got.DefaultTarget)
	}
}

func TestConfigUseUnknownTarget(t *testing.T) {
	dir := t.TempDir()
	st := &state.BridgeState{
		Version:       state.CurrentVersion,
		DefaultTarget: "dev1",
		KnownDaemons:  map[string]state.DaemonRef{"dev1": {DeviceID: "d1", Pubkey: "p1"}},
	}
	_ = state.SaveBridge(filepath.Join(dir, "state.json"), st)
	g.stateDir = dir
	defer func() { g.stateDir = "" }()

	cmd := newConfigUseCmd()
	err := cmd.RunE(cmd, []string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown_target") {
		t.Fatalf("expected unknown_target, got: %v", err)
	}
}
