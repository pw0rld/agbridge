package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newStatusCmd dials the gateway, completes the Hello handshake (+ Noise
// IK if E2E enabled), and tears the connection down. No tool call. The
// elapsed handshake time is reported as a quick liveness/latency check.
// Useful as the first command a new user runs after enroll: "did
// everything wire up?".
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check gateway connectivity (dial + handshake only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runStatus(cmd, opts)
		},
	}
}

// runStatus is the testable inner body. Accepts the resolved opts so
// fixture tests can inject TLSConfig (env.newCLIOpts()) instead of
// dialing the production CertPin path.
func runStatus(cmd *cobra.Command, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	start := time.Now()
	_, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		// status is connect-only; no handler call.
		return nil, nil
	})
	elapsed := time.Since(start)

	if err != nil {
		if g.json {
			printJSON(cmd.OutOrStdout(), map[string]any{
				"target":       opts.Target,
				"gateway":      opts.GatewayURL,
				"agent":        opts.AgentName,
				"version":      agbridgeVersion,
				"connection":   "failed",
				"handshake_ms": float64(elapsed.Microseconds()) / 1000.0,
				"error":        err.Error(),
			})
		}
		printErrorJSON(cmd.ErrOrStderr(), "handshake_failed", err.Error(), true)
		return err
	}

	handshakeMs := float64(elapsed.Microseconds()) / 1000.0
	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{
			"target":       opts.Target,
			"gateway":      opts.GatewayURL,
			"agent":        opts.AgentName,
			"version":      agbridgeVersion,
			"connection":   "ok",
			"handshake_ms": handshakeMs,
		})
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "target:      %s\n", opts.Target)
	fmt.Fprintf(cmd.OutOrStdout(), "gateway:     %s\n", opts.GatewayURL)
	fmt.Fprintf(cmd.OutOrStdout(), "agent:       %s\n", opts.AgentName)
	fmt.Fprintf(cmd.OutOrStdout(), "version:     %s\n", agbridgeVersion)
	fmt.Fprintf(cmd.OutOrStdout(), "connection:  ok (handshake %.1f ms)\n", handshakeMs)
	return nil
}
