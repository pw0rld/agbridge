package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/fsproto"
)

// newStatCmd returns one-shot metadata for a path on the daemon
// (StatHandler). Output is type / size / mode / mod_time in the human
// table; the JSON shape mirrors fsproto.DirEntry plus the original path.
func newStatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stat <path>",
		Short: "Show file metadata on the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runStat(cmd, args[0], opts)
		},
	}
}

// runStat is the testable inner body.
func runStat(cmd *cobra.Command, path string, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	resolved, err := resolveArg(ctx, opts, path)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	path = resolved

	res, err := invokeRouted(ctx, opts, "stat", map[string]any{"path": path})
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "network_lost", err.Error(), true)
		return err
	}
	rmap, ok := res.(map[string]any)
	if !ok {
		printErrorJSON(cmd.ErrOrStderr(), "protocol_error", "unexpected handler shape", false)
		return fmt.Errorf("unexpected handler shape: %T", res)
	}
	if isErr, _ := rmap["isError"].(bool); isErr {
		code, message := toolErrorFields(rmap)
		printErrorJSON(cmd.ErrOrStderr(), code, message, false)
		return fmt.Errorf("%s: %s", code, message)
	}

	entry, ok := extractStatEntry(rmap)
	if !ok {
		printErrorJSON(cmd.ErrOrStderr(), "protocol_error", "stat entry missing from response", false)
		return fmt.Errorf("stat entry missing from response")
	}

	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{
			"path":     path,
			"name":     entry.Name,
			"type":     entry.Type,
			"size":     entry.Size,
			"mode":     entry.Mode,
			"mod_time": entry.ModTime,
		})
		return nil
	}
	modStr := time.Unix(entry.ModTime, 0).UTC().Format(time.RFC3339)
	fmt.Fprintf(cmd.OutOrStdout(), "name:     %s\n", entry.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "type:     %s\n", entry.Type)
	fmt.Fprintf(cmd.OutOrStdout(), "size:     %d\n", entry.Size)
	fmt.Fprintf(cmd.OutOrStdout(), "mode:     %#o\n", entry.Mode)
	fmt.Fprintf(cmd.OutOrStdout(), "mod_time: %s\n", modStr)
	return nil
}

// extractStatEntry pulls the typed *fsproto.DirEntry from the handler
// _meta envelope. StatHandler stores the pointer directly.
func extractStatEntry(res map[string]any) (fsproto.DirEntry, bool) {
	meta, ok := res["_meta"].(map[string]any)
	if !ok {
		return fsproto.DirEntry{}, false
	}
	switch e := meta["entry"].(type) {
	case *fsproto.DirEntry:
		if e == nil {
			return fsproto.DirEntry{}, false
		}
		return *e, true
	case fsproto.DirEntry:
		return e, true
	}
	return fsproto.DirEntry{}, false
}
