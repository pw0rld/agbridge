package state

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBridgeStateRoundTrip(t *testing.T) {
	in := &BridgeState{
		Version:             1,
		DeviceID:            "d_test_bridge",
		AgentName:           "test-laptop",
		APIKey:              "ak_test",
		Gateway:             GatewayRef{URL: "wss://gw.test/", CertPin: "sha256:abc"},
		BridgeStaticKeyPath: "bridge.key",
		E2EMode:             "required",
		DefaultTarget:       "lab01",
		KnownDaemons: map[string]DaemonRef{
			"lab01": {
				DeviceID:  "d_test_daemon",
				Pubkey:    "AGv0Lr8m...",
				PinSource: "control-plane",
				FirstSeen: time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC),
			},
		},
		EnrolledAt: time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := &BridgeState{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if out.AgentName != in.AgentName {
		t.Fatalf("agent_name mismatch: got %q want %q", out.AgentName, in.AgentName)
	}
	if out.KnownDaemons["lab01"].Pubkey != "AGv0Lr8m..." {
		t.Fatal("daemon pubkey lost in roundtrip")
	}
}

func TestDaemonStateRoundTrip(t *testing.T) {
	in := &DaemonState{
		Version:              1,
		DeviceID:             "d_test_daemon",
		DaemonName:           "lab01",
		APIKey:               "ak_test",
		Gateway:              GatewayRef{URL: "wss://gw.test/", CertPin: "sha256:abc"},
		NoiseStaticKeyPath:   "daemon.key",
		E2EMode:              "required",
		AllowedExecCwds:      []string{"/home/me/projects"},
		AllowedReadPaths:     []string{"/home/me/projects"},
		AllowedWritePaths:    []string{"/home/me/projects"},
		EnvAllowlist:         []string{"PATH", "HOME"},
		ForbiddenPorts:       []int{22},
		AllowedBridgePubkeys: []string{"pubA", "pubB"},
		EnrolledAt:           time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC),
	}
	b, _ := json.Marshal(in)
	out := &DaemonState{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if len(out.AllowedBridgePubkeys) != 2 {
		t.Fatalf("allowed_bridge_pubkeys lost: %v", out.AllowedBridgePubkeys)
	}
}

func TestGatewayStateRoundTrip(t *testing.T) {
	in := &GatewayState{
		Version:         1,
		Listen:          ":443",
		AuditPath:       "/var/log/agbridge.jsonl",
		AuditMaxBytes:   1 << 20,
		AuditMaxBackups: 3,
		Agents: []AgentEntry{
			{Name: "alice", APIKeyHash: "sha256:1", AllowedDaemons: []string{"lab01"}},
		},
		Daemons: []DaemonEntry{
			{Name: "lab01", TokenHash: "sha256:2"},
		},
	}
	b, _ := json.Marshal(in)
	out := &GatewayState{}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if out.Agents[0].Name != "alice" {
		t.Fatal("agent lost")
	}
}
