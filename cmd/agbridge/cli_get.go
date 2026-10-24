package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newGetCmd downloads a file from the daemon via DownloadHandler.
// Uses dest_path streaming: the cliclient writes chunks straight to
// the bridge-local file as they arrive, so multi-GiB downloads don't
// buffer everything in memory.
func newGetCmd() *cobra.Command {
	var progress bool
	cmd := &cobra.Command{
		Use:   "get <remote> <local>",
		Short: "Download a file from the daemon",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runGet(cmd, args[0], args[1], opts)
		},
	}
	// TODO(v0.0.3): wire --progress to a byte counter on stderr.
	cmd.Flags().BoolVar(&progress, "progress", false, "(placeholder) show byte counter on stderr")
	return cmd
}

// runGet is the testable inner body.
func runGet(cmd *cobra.Command, remotePath, localPath string, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	resolved, err := resolveArg(ctx, opts, remotePath)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	remotePath = resolved

	res, err := invokeRouted(ctx, opts, "download", map[string]any{
		"src_path":  remotePath,
		"dest_path": localPath,
	})
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

	meta, _ := rmap["_meta"].(map[string]any)
	var size int64
	if s, ok := meta["size"].(int64); ok {
		size = s
	} else if s, ok := meta["size"].(float64); ok {
		size = int64(s)
	}
	sha, _ := meta["sha256"].(string)

	if g.json {
		out := map[string]any{
			"remote": remotePath,
			"local":  localPath,
			"size":   size,
			"sha256": sha,
		}
		// Pass through the segment count when the transfer was served
		// by the parallel segmented downloader (size >= 16 MiB), so
		// scripts can verify which path ran.
		if seg, ok := meta["segments"]; ok {
			out["segments"] = seg
		}
		printJSON(cmd.OutOrStdout(), out)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "downloaded %d bytes to %s (sha256=%s)\n", size, localPath, sha)
	return nil
}
