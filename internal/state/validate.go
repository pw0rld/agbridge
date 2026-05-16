package state

import "fmt"

// Validate checks BridgeState fields for self-consistency.
func (s *BridgeState) Validate() error {
	if s.AgentName == "" {
		return fmt.Errorf("state: agent_name is required")
	}
	if s.APIKey == "" {
		return fmt.Errorf("state: api_key is required")
	}
	if s.Gateway.URL == "" {
		return fmt.Errorf("state: gateway.url is required")
	}
	if err := validateE2EMode(s.E2EMode); err != nil {
		return err
	}
	if s.E2EMode != "disabled" && s.BridgeStaticKeyPath == "" {
		return fmt.Errorf("state: bridge_static_key_path required when e2e_mode=%s", s.E2EMode)
	}
	return nil
}

// Validate checks DaemonState fields for self-consistency.
func (s *DaemonState) Validate() error {
	if s.DaemonName == "" {
		return fmt.Errorf("state: daemon_name is required")
	}
	if s.APIKey == "" {
		return fmt.Errorf("state: api_key is required")
	}
	if s.Gateway.URL == "" {
		return fmt.Errorf("state: gateway.url is required")
	}
	if err := validateE2EMode(s.E2EMode); err != nil {
		return err
	}
	if s.E2EMode != "disabled" && s.NoiseStaticKeyPath == "" {
		return fmt.Errorf("state: noise_static_key_path required when e2e_mode=%s", s.E2EMode)
	}
	if s.E2EMode == "required" && len(s.AllowedBridgePubkeys) == 0 {
		return fmt.Errorf("state: allowed_bridge_pubkeys must be non-empty when e2e_mode=required")
	}
	return nil
}

// Validate checks GatewayState fields.
func (s *GatewayState) Validate() error {
	if s.Listen == "" {
		return fmt.Errorf("state: gateway listen address is required")
	}
	if s.AuditPath == "" {
		return fmt.Errorf("state: gateway audit_path is required")
	}
	return nil
}

func validateE2EMode(mode string) error {
	switch mode {
	case "disabled", "optional", "required":
		return nil
	default:
		return fmt.Errorf("state: invalid e2e_mode %q (want disabled|optional|required)", mode)
	}
}
