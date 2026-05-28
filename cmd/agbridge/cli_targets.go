package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/state"
)

// newTargetsCmd lists daemons recorded in the local bridge state.json.
// No network: this is a pure read of on-disk state, surfacing which
// target the CLI will use by default and the short pubkey we pinned.
func newTargetsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "targets",
		Short: "List known daemon targets from local state",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := state.LoadBridge(stateJSONPath(g.stateDir))
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			if g.json {
				out := []map[string]any{}
				for name, rec := range st.KnownDaemons {
					out = append(out, map[string]any{
						"name":      name,
						"default":   name == st.DefaultTarget,
						"pubkey":    rec.Pubkey,
						"device_id": rec.DeviceID,
						"enrolled":  rec.FirstSeen.Format("2006-01-02T15:04:05Z07:00"),
					})
				}
				printJSON(cmd.OutOrStdout(), map[string]any{"targets": out})
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "NAME\tDEFAULT\tPUBKEY (b64, truncated)\tENROLLED")
			for name, rec := range st.KnownDaemons {
				mark := "-"
				if name == st.DefaultTarget {
					mark = "*"
				}
				pk := rec.Pubkey
				if len(pk) > 20 {
					pk = pk[:20] + "..."
				}
				when := rec.FirstSeen.Format("2006-01-02")
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", name, mark, pk, when)
			}
			return nil
		},
	}
}
