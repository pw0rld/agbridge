package state

import (
	"strings"
	"testing"
)

func TestBridgeStateValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*BridgeState)
		wantErr string
	}{
		{"valid", func(*BridgeState) {}, ""},
		{"missing_api_key", func(s *BridgeState) { s.APIKey = "" }, "api_key"},
		{"missing_gateway_url", func(s *BridgeState) { s.Gateway.URL = "" }, "gateway.url"},
		{"bad_e2e_mode", func(s *BridgeState) { s.E2EMode = "yolo" }, "e2e_mode"},
		{"e2e_required_no_key", func(s *BridgeState) {
			s.E2EMode = "required"
			s.BridgeStaticKeyPath = ""
		}, "bridge_static_key_path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validBridge()
			tt.mut(s)
			err := s.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestDaemonStateValidateRequiredACL(t *testing.T) {
	s := validDaemon()
	s.E2EMode = "required"
	s.AllowedBridgePubkeys = nil
	if err := s.Validate(); err == nil {
		t.Fatal("expected error: required mode + empty allowlist")
	}
}

func TestDaemonStateValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*DaemonState)
		wantErr string
	}{
		{"valid", func(*DaemonState) {}, ""},
		{"missing_name", func(s *DaemonState) { s.DaemonName = "" }, "daemon_name"},
		{"bad_e2e", func(s *DaemonState) { s.E2EMode = "xyz" }, "e2e_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validDaemon()
			tt.mut(s)
			err := s.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestGatewayStateValidate(t *testing.T) {
	s := &GatewayState{Version: 1, Listen: "", AuditPath: "/dev/null"}
	if err := s.Validate(); err == nil {
		t.Fatal("expected error on missing listen")
	}
	s.Listen = ":443"
	s.AuditPath = ""
	if err := s.Validate(); err == nil {
		t.Fatal("expected error on missing audit_path")
	}
	s.AuditPath = "/dev/null"
	if err := s.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func validBridge() *BridgeState {
	return &BridgeState{
		Version:             1,
		DeviceID:            "d_b",
		AgentName:           "x",
		APIKey:              "k",
		Gateway:             GatewayRef{URL: "wss://gw/", CertPin: "sha256:abc"},
		BridgeStaticKeyPath: "bridge.key",
		E2EMode:             "disabled",
	}
}

func validDaemon() *DaemonState {
	return &DaemonState{
		Version:            1,
		DeviceID:           "d_d",
		DaemonName:         "lab",
		APIKey:             "k",
		Gateway:            GatewayRef{URL: "wss://gw/", CertPin: "sha256:abc"},
		NoiseStaticKeyPath: "daemon.key",
		E2EMode:            "disabled",
	}
}
