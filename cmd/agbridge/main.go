package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "agbridge",
		Short:   "AI agent remote operation surface over restrictive networks",
		Version: agbridgeVersion,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	registerGlobalFlags(root)
	root.AddCommand(
		newBridgeCmd(), newGatewayCmd(), newDaemonCmd(),
		newCertCmd(), newKeygenCmd(),
		newIssueTokenCmd(), newEnrollCmd(),
		newConfigCmd(), newLogoutCmd(), newDoctorCmd(),
		newGatewayListDevicesCmd(), newGatewayRevokeCmd(),
		// CLI subcommands (v0.0.2)
		newTargetsCmd(),
		newAIHelpCmd(),
		newStatusCmd(),
		newCapsCmd(),
		newLsCmd(),
		newCatCmd(),
		newStatCmd(),
		newGrepCmd(),
		newWriteCmd(),
		newEditCmd(),
		newExecCmd(),
		newPutCmd(),
		newGetCmd(),
	)
	return root
}
