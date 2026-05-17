package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/gateway"
)

// newIssueTokenCmd implements `agbridge issue-token`. Gateway operators
// run this to mint a one-shot enrollment token, which they then send
// to the device operator as a paste-command.
func newIssueTokenCmd() *cobra.Command {
	var (
		cfgPath        string
		role           string
		name           string
		target         string
		allowedPaths   []string
		forbiddenPorts []int
		ttlMinutes     int
		emitJSON       bool
	)
	cmd := &cobra.Command{
		Use:   "issue-token",
		Short: "Issue a one-shot enrollment token for a bridge or daemon",
		Long: `issue-token mints a short-lived (default 15min, one-time-use) token
that a device operator pastes alongside agbridge enroll. For daemon
tokens, --allowed-paths and --forbidden-ports are baked into the token;
the daemon receives them from the gateway at enroll time and cannot
widen the policy on its end.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if role != "bridge" && role != "daemon" {
				return fmt.Errorf("--role must be bridge or daemon")
			}
			if name == "" {
				return fmt.Errorf("--name required")
			}
			if role == "bridge" && target == "" {
				return fmt.Errorf("--target required for bridge tokens")
			}
			cfg, err := config.LoadGateway(cfgPath)
			if err != nil {
				return err
			}
			tokensPath := filepath.Join(filepath.Dir(cfgPath), "tokens.json")
			store, err := gateway.NewTokenStore(tokensPath)
			if err != nil {
				return err
			}
			ttl := time.Duration(ttlMinutes) * time.Minute
			if ttl <= 0 {
				ttl = 15 * time.Minute
			}
			req := gateway.TokenRequest{Role: role, Name: name, Target: target, TTL: ttl}
			if role == "daemon" {
				req.Policy = &gateway.DaemonPolicy{
					AllowedPaths:   allowedPaths,
					ForbiddenPorts: forbiddenPorts,
				}
			}
			tok, err := store.Issue(req)
			if err != nil {
				return err
			}

			publicURL := cfg.PublicURL
			if publicURL == "" {
				publicURL = "wss://" + cfg.Listen + "/"
			}

			if emitJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"token":       tok.Token,
					"gateway":     publicURL,
					"expires_at":  tok.ExpiresAt,
					"role":        role,
					"name":        name,
					"paste_command": pasteCommand(publicURL, tok.Token),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Token issued for %s role=%s (expires %s)\n\n",
				name, role, tok.ExpiresAt.Format(time.RFC3339))
			fmt.Fprintf(cmd.OutOrStdout(), "Paste on the %s machine:\n\n  %s\n\n",
				role, strings.TrimSpace(pasteCommand(publicURL, tok.Token)))
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to gateway YAML")
	_ = cmd.MarkFlagRequired("config")
	cmd.Flags().StringVar(&role, "role", "", "bridge or daemon")
	cmd.Flags().StringVar(&name, "name", "", "device name (unique within tenant)")
	cmd.Flags().StringVar(&target, "target", "", "target daemon name (for bridge tokens)")
	cmd.Flags().StringSliceVar(&allowedPaths, "allowed-paths", nil, "daemon-only: filesystem paths the daemon may exec/read/write (repeatable)")
	cmd.Flags().IntSliceVar(&forbiddenPorts, "forbidden-ports", nil, "daemon-only: TCP ports port_forward refuses (repeatable)")
	cmd.Flags().IntVar(&ttlMinutes, "ttl", 15, "token TTL in minutes")
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit machine-readable JSON")
	return cmd
}

func pasteCommand(publicURL, tok string) string {
	return fmt.Sprintf(
		`curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash && agbridge enroll --gateway %s --token %s`,
		publicURL, tok,
	)
}
