package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "agbridge",
		Short: "AI agent remote operation surface over restrictive networks",
	}
	root.AddCommand(newBridgeCmd(), newGatewayCmd(), newDaemonCmd(), newCertCmd(), newKeygenCmd(), newBootstrapCmd(), newIssueTokenCmd(), newEnrollCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
