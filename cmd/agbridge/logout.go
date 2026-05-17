package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newLogoutCmd wipes local state files. It does NOT contact the gateway
// to revoke credentials — that needs `agbridge gateway revoke --name …`
// run by the gateway operator. The disconnect happens automatically when
// the device is next removed from the live CredRegistry.
func newLogoutCmd() *cobra.Command {
	var stateDir string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Delete local state.json and key files",
		RunE: func(cmd *cobra.Command, args []string) error {
			if stateDir == "" {
				stateDir = defaultStateDir()
			}
			for _, name := range []string{"state.json", "bridge_noise.key", "daemon_noise.key"} {
				p := filepath.Join(stateDir, name)
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove %s: %w", p, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStderr(), "Local state in %s removed.\n", stateDir)
			fmt.Fprintln(cmd.OutOrStderr(), "Note: server-side credential is still valid until the gateway operator runs `agbridge gateway revoke`.")
			return nil
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "default ~/.config/agbridge/")
	return cmd
}
