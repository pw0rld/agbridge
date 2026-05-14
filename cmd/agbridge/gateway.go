package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newGatewayCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gateway",
		Short: "Run the gateway server",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("TODO: gateway")
			return nil
		},
	}
}
