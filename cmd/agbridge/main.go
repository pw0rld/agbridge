package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	os.Exit(cliMain(os.Args[1:], os.Stdout, os.Stderr))
}

// jsonError marks an error whose machine-readable envelope was already
// written to stdout by the command itself (--json mode). cliMain must
// not append a second JSON object on top of it.
type jsonError struct{ err error }

func (e jsonError) Error() string { return e.err.Error() }
func (e jsonError) Unwrap() error { return e.err }

// cliMain is main() factored for tests. Exit-code contract: 0 success,
// 1 any error.
//
// Output contract for agents: cobra's own Error/usage spam is silenced,
// so a failure is reported exactly once. In --json mode stdout then
// carries exactly one JSON object — either the command's result or the
// generic {"ok":false,"error":"..."} envelope; stderr carries a single
// human-readable line.
func cliMain(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SilenceErrors = true // cliMain is the single error reporter
	root.SilenceUsage = true  // usage dumps are noise for machines
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		var je jsonError
		if g.json && !errors.As(err, &je) {
			_ = json.NewEncoder(stdout).Encode(map[string]any{
				"ok":    false,
				"error": err.Error(),
			})
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
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
		newGatewayCmd(), newDaemonCmd(),
		newCertCmd(), newKeygenCmd(),
		newIssueTokenCmd(), newEnrollCmd(),
		newConfigCmd(), newLogoutCmd(), newDoctorCmd(),
		newGatewayListDevicesCmd(), newGatewayRevokeCmd(),
		// CLI subcommands (v0.0.2)
		newAgentCmd(),
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
		newCdCmd(),
		newPwdCmd(),
	)
	return root
}
