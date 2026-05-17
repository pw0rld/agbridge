package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/state"
)

// newConfigCmd is the `agbridge config …` parent with the show subcommand.
func newConfigCmd() *cobra.Command {
	parent := &cobra.Command{
		Use:   "config",
		Short: "Inspect local state",
	}
	parent.AddCommand(newConfigShowCmd())
	return parent
}

func newConfigShowCmd() *cobra.Command {
	var stateDir string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print state.json with api_key redacted",
		RunE: func(cmd *cobra.Command, args []string) error {
			if stateDir == "" {
				stateDir = defaultStateDir()
			}
			path := filepath.Join(stateDir, "state.json")
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if bs, err := state.LoadBridge(path); err == nil {
				bs.APIKey = redact(bs.APIKey)
				return enc.Encode(bs)
			}
			ds, err := state.LoadDaemon(path)
			if err != nil {
				return fmt.Errorf("no usable bridge/daemon state at %s: %w", path, err)
			}
			ds.APIKey = redact(ds.APIKey)
			return enc.Encode(ds)
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "default ~/.config/agbridge/")
	return cmd
}

func redact(s string) string {
	if len(s) < 10 {
		return "***"
	}
	return s[:6] + "…" + s[len(s)-4:]
}
