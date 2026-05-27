package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/state"
)

// newConfigUseCmd sets BridgeState.DefaultTarget to <name>. Refuses
// names that are not present in KnownDaemons so a typo doesn't silently
// point the CLI at a daemon we never enrolled.
func newConfigUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the default daemon target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := stateJSONPath(g.stateDir)
			st, err := state.LoadBridge(path)
			if err != nil {
				return err
			}
			if _, ok := st.KnownDaemons[args[0]]; !ok {
				return fmt.Errorf("unknown_target: %s", args[0])
			}
			st.DefaultTarget = args[0]
			if err := state.SaveBridge(path, st); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "default target: %s\n", args[0])
			return nil
		},
	}
}
