package main

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/pw0rld/agbridge/internal/auth"
)

const (
	gatewayTLSModePin    = "pin"
	gatewayTLSModeWebPKI = "webpki"
)

func normalizeGatewayTLSMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", gatewayTLSModePin:
		return gatewayTLSModePin, nil
	case gatewayTLSModeWebPKI:
		return gatewayTLSModeWebPKI, nil
	default:
		return "", fmt.Errorf("gateway tls mode %q invalid (want pin|webpki)", mode)
	}
}

func enrollTLSConfig(mode string) (*tls.Config, error) {
	mode, err := normalizeGatewayTLSMode(mode)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if mode == gatewayTLSModePin {
		// Pin mode intentionally skips WebPKI during enrollment so a
		// self-signed origin certificate can be TOFU-pinned by its DER hash.
		cfg.InsecureSkipVerify = true
	}
	return cfg, nil
}

func runtimeTLSConfig(mode, certPin string) (*tls.Config, error) {
	mode, err := normalizeGatewayTLSMode(mode)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	switch mode {
	case gatewayTLSModePin:
		cfg.InsecureSkipVerify = true
		if err := auth.AttachCertPin(cfg, certPin); err != nil {
			return nil, fmt.Errorf("cert pin: %w", err)
		}
	case gatewayTLSModeWebPKI:
		// Standard hostname + system CA verification. This is the hosted
		// Cloudflare mode: TLS authenticates relay.agbridge.cloud at the edge;
		// bridge<->daemon content secrecy is still provided by Noise E2E.
	}
	return cfg, nil
}
