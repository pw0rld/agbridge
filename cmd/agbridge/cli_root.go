package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/state"
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

// stateJSONPath returns the path to state.json under stateDir, or the
// default ~/.config/agbridge/state.json when stateDir is empty.
func stateJSONPath(stateDir string) string {
	if stateDir != "" {
		return filepath.Join(stateDir, "state.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "state.json"
	}
	return filepath.Join(home, ".config", "agbridge", "state.json")
}

// loadOpts builds cliclient.Options from on-disk state.json + the
// global -t target override. Returns "unknown_target" error when -t
// names a daemon not present in state.
func loadOpts() (cliclient.Options, error) {
	path := stateJSONPath(g.stateDir)
	st, err := state.LoadBridge(path)
	if err != nil {
		return cliclient.Options{}, fmt.Errorf("load %s: %w", path, err)
	}
	target := st.DefaultTarget
	if g.target != "" {
		target = g.target
	}
	rec, ok := st.KnownDaemons[target]
	if !ok {
		return cliclient.Options{}, fmt.Errorf("unknown_target: %s", target)
	}
	return cliclient.Options{
		GatewayURL:          st.Gateway.URL,
		AgentName:           st.AgentName,
		APIKey:              st.APIKey,
		CertPin:             st.Gateway.CertPin,
		GatewayTLSMode:      st.Gateway.TLSMode,
		E2EMode:             st.E2EMode,
		BridgeStaticKeyPath: st.BridgeStaticKeyPath,
		DaemonPubkey:        rec.Pubkey,
		Target:              target,
	}, nil
}
