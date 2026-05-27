package auth

import (
	"crypto/tls"
	"fmt"
	"strings"
)

const (
	GatewayTLSModePin    = "pin"
	GatewayTLSModeWebPKI = "webpki"
)

// NormalizeGatewayTLSMode canonicalizes user-provided TLS mode strings
// to one of GatewayTLSModePin / GatewayTLSModeWebPKI.
func NormalizeGatewayTLSMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", GatewayTLSModePin:
		return GatewayTLSModePin, nil
	case GatewayTLSModeWebPKI:
		return GatewayTLSModeWebPKI, nil
	default:
		return "", fmt.Errorf("gateway tls mode %q invalid (want pin|webpki)", mode)
	}
}

// EnrollTLSConfig returns the TLS config used during enrollment.
// Pin mode skips WebPKI verification so a self-signed origin
// certificate can be TOFU-pinned by its DER hash.
func EnrollTLSConfig(mode string) (*tls.Config, error) {
	mode, err := NormalizeGatewayTLSMode(mode)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if mode == GatewayTLSModePin {
		// Pin mode intentionally skips WebPKI during enrollment so a
		// self-signed origin certificate can be TOFU-pinned by its DER hash.
		cfg.InsecureSkipVerify = true
	}
	return cfg, nil
}

// RuntimeTLSConfig returns the TLS config used by long-running bridge
// and CLI sessions. In pin mode it attaches the cert pin verifier; in
// webpki mode it leaves standard hostname + system CA verification in
// place.
func RuntimeTLSConfig(mode, certPin string) (*tls.Config, error) {
	mode, err := NormalizeGatewayTLSMode(mode)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	switch mode {
	case GatewayTLSModePin:
		cfg.InsecureSkipVerify = true
		if err := AttachCertPin(cfg, certPin); err != nil {
			return nil, fmt.Errorf("cert pin: %w", err)
		}
	case GatewayTLSModeWebPKI:
		// Standard hostname + system CA verification. This is the hosted
		// Cloudflare mode: TLS authenticates relay.agbridge.cloud at the edge;
		// bridge<->daemon content secrecy is still provided by Noise E2E.
	}
	return cfg, nil
}
