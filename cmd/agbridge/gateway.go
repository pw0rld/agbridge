package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/audit"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
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
			inst, err := gateway.Run(ctx, tlsCfg, cfg, aud)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "gateway listening on %s\n", inst.Addr)

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
