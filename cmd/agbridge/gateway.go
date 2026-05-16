package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/state"
)

func newGatewayCmd() *cobra.Command {
	var (
		cfgPath  string
		certPath string
		keyPath  string
	)
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Run the gateway server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGateway(cfgPath)
			if err != nil {
				return err
			}
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				return fmt.Errorf("load tls keypair: %w", err)
			}
			tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}

			// Compute the cert SHA-256 pin once so enroll responses can include
			// it. Reads the PEM directly (cert.Leaf is nil unless we parse).
			certPEM, err := os.ReadFile(certPath)
			if err != nil {
				return fmt.Errorf("read cert: %w", err)
			}
			pin, err := computeCertPin(certPEM)
			if err != nil {
				return err
			}

			aud, err := audit.OpenWith(cfg.AuditPath, audit.Options{
				MaxBytes:   cfg.AuditMaxBytes,
				MaxBackups: cfg.AuditMaxBackups,
			})
			if err != nil {
				return fmt.Errorf("open audit: %w", err)
			}
			defer aud.Close()
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			// Wire enrollment endpoint on /v1/enroll (shared 443 TLS server).
			cfgDir := filepath.Dir(cfgPath)
			tokensPath := filepath.Join(cfgDir, "tokens.json")
			gwStatePath := filepath.Join(cfgDir, "gateway-state.json")
			tokenStore, err := gateway.NewTokenStore(tokensPath)
			if err != nil {
				return fmt.Errorf("token store: %w", err)
			}
			gwState := hydrateGatewayState(cfg, tokensPath)
			// If a prior gateway-state.json exists, merge its entries
			// (devices enrolled in previous runs).
			if prior, err := state.LoadGateway(gwStatePath); err == nil {
				mergeGatewayState(gwState, prior, cfg)
			}
			publicURL := cfg.PublicURL
			if publicURL == "" {
				publicURL = "wss://" + cfg.Listen + "/"
			}
			enrollSrv := &gateway.EnrollServer{
				Tokens:        tokenStore,
				State:         gwState,
				StatePath:     gwStatePath,
				GatewayURL:    publicURL,
				CertPinSource: func() string { return pin },
			}

			mux := http.NewServeMux()
			mux.HandleFunc("/v1/enroll", enrollSrv.HandleEnroll)

			inst, err := gateway.RunWithHandler(ctx, tlsCfg, cfg, aud, mux)
			if err != nil {
				return err
			}
			// Wire EnrollServer to the live CredRegistry so onboarded
			// devices take effect immediately (no SIGHUP needed).
			enrollSrv.Live = inst.Creds
			fmt.Fprintf(os.Stderr, "gateway listening on %s (enroll endpoint at /v1/enroll)\n", inst.Addr)

			hupCh := make(chan os.Signal, 1)
			signal.Notify(hupCh, syscall.SIGHUP)
			defer signal.Stop(hupCh)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-hupCh:
						newCfg, err := config.LoadGateway(cfgPath)
						if err != nil {
							fmt.Fprintf(os.Stderr, "SIGHUP: reload failed: %v\n", err)
							continue
						}
						inst.Creds.Replace(newCfg)
						revoked := inst.Sessions.Revoke(inst.Creds)
						if len(revoked) > 0 {
							fmt.Fprintf(os.Stderr, "SIGHUP: reloaded; revoked %d sessions: %v\n", len(revoked), revoked)
						} else {
							fmt.Fprintln(os.Stderr, "SIGHUP: reloaded, no revocations")
						}
					}
				}
			}()

			<-ctx.Done()
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to gateway YAML config")
	cmd.Flags().StringVar(&certPath, "cert", "", "path to PEM cert file")
	cmd.Flags().StringVar(&keyPath, "key", "", "path to PEM key file")
	_ = cmd.MarkFlagRequired("config")
	_ = cmd.MarkFlagRequired("cert")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

// computeCertPin returns sha256:<hex> over the first cert in the PEM bundle.
func computeCertPin(pemBytes []byte) (string, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("cert: PEM decode failed")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("cert: parse: %w", err)
	}
	sum := sha256.Sum256(c.Raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// hydrateGatewayState builds an initial state.GatewayState from the
// loaded yaml config. EnrollServer mutates this struct as devices
// enroll, and persists to gateway-state.json so the additions survive
// restart.
func hydrateGatewayState(cfg *config.GatewayConfig, tokensPath string) *state.GatewayState {
	gs := &state.GatewayState{
		Version:         state.CurrentVersion,
		Listen:          cfg.Listen,
		AuditPath:       cfg.AuditPath,
		AuditMaxBytes:   cfg.AuditMaxBytes,
		AuditMaxBackups: cfg.AuditMaxBackups,
		TokensPath:      tokensPath,
	}
	for _, a := range cfg.Agents {
		gs.Agents = append(gs.Agents, state.AgentEntry{
			Name:           a.Name,
			APIKeyHash:     a.APIKeyHash,
			AllowedDaemons: a.AllowedDaemons,
		})
	}
	for _, d := range cfg.Daemons {
		gs.Daemons = append(gs.Daemons, state.DaemonEntry{
			Name:      d.Name,
			TokenHash: d.TokenHash,
		})
	}
	return gs
}

// mergeGatewayState folds entries from a previously-persisted gateway-state.json
// into the freshly-hydrated state and into the loaded config so that
// CredRegistry sees them. Only adds entries not already present by name.
func mergeGatewayState(into *state.GatewayState, prior *state.GatewayState, cfg *config.GatewayConfig) {
	have := map[string]bool{}
	for _, a := range into.Agents {
		have["a/"+a.Name] = true
	}
	for _, d := range into.Daemons {
		have["d/"+d.Name] = true
	}
	for _, a := range prior.Agents {
		if !have["a/"+a.Name] {
			into.Agents = append(into.Agents, a)
			cfg.Agents = append(cfg.Agents, config.AgentEntry{
				Name:           a.Name,
				APIKeyHash:     a.APIKeyHash,
				AllowedDaemons: a.AllowedDaemons,
			})
		}
	}
	for _, d := range prior.Daemons {
		if !have["d/"+d.Name] {
			into.Daemons = append(into.Daemons, d)
			cfg.Daemons = append(cfg.Daemons, config.DaemonEntry{
				Name:      d.Name,
				TokenHash: d.TokenHash,
			})
		}
	}
}
