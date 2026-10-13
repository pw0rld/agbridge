package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Keep an explicit error for users who paste a daemon command on Windows.
func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("Windows supports the agbridge CLI client only; run the daemon on Linux or macOS")
		},
	}
}
