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
	root.AddCommand(
		newBridgeCmd(), newGatewayCmd(), newDaemonCmd(),
		newCertCmd(), newKeygenCmd(),
		newIssueTokenCmd(), newEnrollCmd(),
		newConfigCmd(), newLogoutCmd(), newDoctorCmd(),
		newGatewayListDevicesCmd(), newGatewayRevokeCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
