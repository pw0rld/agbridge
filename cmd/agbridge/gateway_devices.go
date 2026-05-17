package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/state"
)

func newGatewayListDevicesCmd() *cobra.Command {
	var cfgPath string
	var emitJSON bool
	cmd := &cobra.Command{
		Use:   "gateway-list-devices",
		Short: "List enrolled bridges and daemons plus outstanding tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadGateway(cfgPath)
			if err != nil {
				return err
			}
			var prior *state.GatewayState
			if p, err := state.LoadGateway(gatewayStatePath(cfgPath)); err == nil {
				prior = p
			}
			tokens, err := gateway.NewTokenStore(gatewayTokensPath(cfgPath))
			if err != nil {
				return err
			}

			agents := append([]config.AgentEntry{}, cfg.Agents...)
			daemons := append([]config.DaemonEntry{}, cfg.Daemons...)
			if prior != nil {
				have := map[string]bool{}
				for _, a := range agents {
					have["a/"+a.Name] = true
				}
				for _, d := range daemons {
					have["d/"+d.Name] = true
				}
				for _, a := range prior.Agents {
					if !have["a/"+a.Name] {
						agents = append(agents, config.AgentEntry{Name: a.Name, APIKeyHash: a.APIKeyHash, AllowedDaemons: a.AllowedDaemons})
					}
				}
				for _, d := range prior.Daemons {
					if !have["d/"+d.Name] {
						daemons = append(daemons, config.DaemonEntry{Name: d.Name, TokenHash: d.TokenHash})
					}
				}
			}

			toks := tokens.List()
			out := map[string]any{"agents": agents, "daemons": daemons, "tokens": toks}
			if emitJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Agents (bridges):")
			for _, a := range agents {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s  allowed_daemons=%v\n", a.Name, a.AllowedDaemons)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Daemons:")
			for _, d := range daemons {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", d.Name)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Outstanding tokens:")
			for _, t := range toks {
				status := "active"
				if t.Used {
					status = "used"
				} else if time.Now().After(t.ExpiresAt) {
					status = "expired"
				}
				suffix := t.Token
				if len(suffix) > 14 {
					suffix = suffix[:14] + "…"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s  role=%s  name=%s  status=%s  expires=%s\n",
					suffix, t.Role, t.Name, status, t.ExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to gateway YAML")
	_ = cmd.MarkFlagRequired("config")
	cmd.Flags().BoolVar(&emitJSON, "json", false, "machine-readable JSON")
	return cmd
}

func newGatewayRevokeCmd() *cobra.Command {
	var cfgPath, name string
	cmd := &cobra.Command{
		Use:   "gateway-revoke",
		Short: "Revoke an enrolled bridge or daemon",
		Long: `Removes the named device from gateway-state.json. Send SIGHUP to the
running gateway (kill -HUP <pid>) to drop active sessions for the
revoked principal. If gateway-state.json does not yet exist or does not
contain the name, the operator can also edit gateway.yaml directly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name required")
			}
			path := gatewayStatePath(cfgPath)
			prior, err := state.LoadGateway(path)
			if err != nil {
				return fmt.Errorf("load %s: %w (no devices enrolled yet?)", path, err)
			}
			before := len(prior.Agents) + len(prior.Daemons)
			prior.Agents = filterAgents(prior.Agents, name)
			prior.Daemons = filterDaemons(prior.Daemons, name)
			after := len(prior.Agents) + len(prior.Daemons)
			if after == before {
				return fmt.Errorf("no enrolled device named %q in %s", name, path)
			}
			if err := state.SaveGateway(path, prior); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStderr(), "Revoked %s from %s.\n", name, path)
			fmt.Fprintln(cmd.OutOrStderr(), "Send SIGHUP to the running gateway to drop active sessions:")
			fmt.Fprintln(cmd.OutOrStderr(), "  pkill -HUP -f 'agbridge gateway'")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to gateway YAML")
	_ = cmd.MarkFlagRequired("config")
	cmd.Flags().StringVar(&name, "name", "", "device name to revoke")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func filterAgents(in []state.AgentEntry, drop string) []state.AgentEntry {
	out := in[:0]
	for _, a := range in {
		if a.Name != drop {
			out = append(out, a)
		}
	}
	return out
}
func filterDaemons(in []state.DaemonEntry, drop string) []state.DaemonEntry {
	out := in[:0]
	for _, d := range in {
		if d.Name != drop {
			out = append(out, d)
		}
	}
	return out
}

// silence unused import if list-devices doesn't reach the os branch
var _ = os.Stderr
