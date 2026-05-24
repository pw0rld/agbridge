package main

import "testing"

func TestNormalizeGatewayTLSMode(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", gatewayTLSModePin, false},
		{"pin", gatewayTLSModePin, false},
		{" PIN ", gatewayTLSModePin, false},
		{"webpki", gatewayTLSModeWebPKI, false},
		{"WebPKI", gatewayTLSModeWebPKI, false},
		{"off", "", true},
	}
	for _, tt := range tests {
		got, err := normalizeGatewayTLSMode(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("normalizeGatewayTLSMode(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalizeGatewayTLSMode(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("normalizeGatewayTLSMode(%q): got %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestEnrollTLSConfigModes(t *testing.T) {
	pinCfg, err := enrollTLSConfig(gatewayTLSModePin)
	if err != nil {
		t.Fatalf("pin config: %v", err)
	}
	if !pinCfg.InsecureSkipVerify {
		t.Fatal("pin enrollment should skip WebPKI so self-signed origins can be pinned")
	}

	webpkiCfg, err := enrollTLSConfig(gatewayTLSModeWebPKI)
	if err != nil {
		t.Fatalf("webpki config: %v", err)
	}
	if webpkiCfg.InsecureSkipVerify {
		t.Fatal("webpki mode must not disable certificate verification")
	}
}

func TestRuntimeTLSConfigModes(t *testing.T) {
	pinCfg, err := runtimeTLSConfig(gatewayTLSModePin, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("pin config: %v", err)
	}
	if !pinCfg.InsecureSkipVerify || pinCfg.VerifyConnection == nil {
		t.Fatal("pin runtime config should use VerifyConnection pin check")
	}

	webpkiCfg, err := runtimeTLSConfig(gatewayTLSModeWebPKI, "")
	if err != nil {
		t.Fatalf("webpki config: %v", err)
	}
	if webpkiCfg.InsecureSkipVerify {
		t.Fatal("webpki mode must use normal certificate verification")
	}
	if webpkiCfg.VerifyConnection != nil {
		t.Fatal("webpki mode should not attach origin cert pin verification")
	}
}
