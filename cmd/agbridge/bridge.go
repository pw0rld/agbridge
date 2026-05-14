package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newBridgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bridge",
		Short: "Run the local MCP bridge",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("TODO: bridge")
			return nil
		},
	}
}
