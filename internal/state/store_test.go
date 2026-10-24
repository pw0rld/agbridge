package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadBridgeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	in := &BridgeState{Version: 1, DeviceID: "d_x", AgentName: "x", APIKey: "k"}
	if err := SaveBridge(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600 perms, got %o", perm)
	}
	out, err := LoadBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.AgentName != "x" {
		t.Fatalf("name mismatch: %q", out.AgentName)
	}
}

func TestSaveAtomicNoPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	in := &BridgeState{Version: 1, DeviceID: "d_x", AgentName: "y", APIKey: "k"}
	if err := SaveBridge(path, in); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover .tmp file: %s", e.Name())
		}
	}
}

func TestLoadRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	raw := map[string]any{"version": 99, "device_id": "d", "agent_name": "x", "api_key": "k"}
	b, _ := json.Marshal(raw)
	_ = os.WriteFile(path, b, 0o600)
	if _, err := LoadBridge(path); err == nil {
		t.Fatal("expected version mismatch error")
	}
}

func TestLoadMissingFileReturnsError(t *testing.T) {
	if _, err := LoadBridge("/nonexistent/state.json"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestLoadBridgeMigratesLegacyWorkingDir verifies the deprecated global
// working_dir field is folded into WorkingDirs[default_target] at load
// time and cleared, without clobbering an existing per-target entry.
func TestLoadBridgeMigratesLegacyWorkingDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	raw := map[string]any{
		"version":        1,
		"device_id":      "d_x",
		"agent_name":     "x",
		"api_key":        "k",
		"default_target": "lab",
		"working_dir":    "/legacy/path",
	}
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := LoadBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.WorkingDir != "" {
		t.Fatalf("legacy field not cleared: %q", st.WorkingDir)
	}
	if st.WorkingDirs["lab"] != "/legacy/path" {
		t.Fatalf("not migrated: %v", st.WorkingDirs)
	}

	// Existing per-target value wins over the legacy field.
	raw["working_dirs"] = map[string]string{"lab": "/already/set"}
	b, _ = json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = LoadBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.WorkingDirs["lab"] != "/already/set" {
		t.Fatalf("migration clobbered existing entry: %v", st.WorkingDirs)
	}
}

func TestSaveLoadDaemonRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	in := &DaemonState{Version: 1, DeviceID: "d", DaemonName: "lab", APIKey: "k"}
	if err := SaveDaemon(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadDaemon(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.DaemonName != "lab" {
		t.Fatalf("daemon name mismatch: %q", out.DaemonName)
	}
}

func TestSaveLoadGatewayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	in := &GatewayState{Version: 1, Listen: ":443", AuditPath: "/dev/null"}
	if err := SaveGateway(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Listen != ":443" {
		t.Fatal("listen lost")
	}
}
