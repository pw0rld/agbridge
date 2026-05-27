package main

import (
	"time"

	"github.com/spf13/cobra"
)

// globalFlags holds the values bound by persistent flags. Read by
// every CLI subcommand that needs target/json/state-dir/timeout.
type globalFlags struct {
	target   string
	json     bool
	stateDir string
	timeout  time.Duration
}

var g globalFlags

func registerGlobalFlags(root *cobra.Command) {
	root.PersistentFlags().StringVarP(&g.target, "target", "t", "",
		"override default daemon target for this invocation")
	root.PersistentFlags().BoolVar(&g.json, "json", false,
		"emit stable JSON on stdout instead of human format")
	root.PersistentFlags().StringVar(&g.stateDir, "state-dir", "",
		"directory containing state.json (default ~/.config/agbridge/)")
	root.PersistentFlags().DurationVar(&g.timeout, "timeout", 60*time.Second,
		"per-call timeout")
}
