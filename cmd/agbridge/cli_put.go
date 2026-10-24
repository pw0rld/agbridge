package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newPutCmd uploads a local file to the daemon via UploadHandler.
// Uses source=local_path (F9.3) so the bridge reads the file off its
// own disk and streams 1 MiB chunks — no inline base64, no extra
// memory pressure on either side. Daemon writes atomically.
func newPutCmd() *cobra.Command {
	var modeStr string
	var progress bool
	cmd := &cobra.Command{
		Use:   "put <local> <remote>",
		Short: "Upload a local file to the daemon",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			mode, err := parseMode(modeStr)
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "bad_payload", err.Error(), false)
				return err
			}
			return runPut(cmd, args[0], args[1], mode, opts)
		},
	}
	cmd.Flags().StringVar(&modeStr, "mode", "0644", "file mode (octal, e.g. 0644); 0 = daemon default")
	// TODO(v0.0.3): wire --progress to a byte counter on stderr.
	cmd.Flags().BoolVar(&progress, "progress", false, "(placeholder) show byte counter on stderr")
	return cmd
}

// parseMode accepts "0644", "644", "0", or "" and returns the parsed
// octal mode. Empty or "0" means "use daemon default" — the field is
// omitted from the JSON args so handler-side zero-handling kicks in.
// Modes above 0777 are rejected: setuid/setgid/sticky bits are never
// honored by the daemon, so accepting them here would only create
// silently-ignored flags (better to fail loudly on explicit intent).
func parseMode(s string) (uint32, error) {
	if s == "" || s == "0" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("--mode %q: %w", s, err)
	}
	if v > 0o777 {
		return 0, fmt.Errorf("--mode %q: permission bits only (0-777); setuid/setgid/sticky are not supported", s)
	}
	return uint32(v), nil
}

// runPut is the testable inner body.
func runPut(cmd *cobra.Command, localPath, remotePath string, mode uint32, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	resolved, err := resolveArg(ctx, opts, remotePath)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	remotePath = resolved

	argsMap := map[string]any{
		"source":     "local_path",
		"dest_path":  remotePath,
		"local_path": localPath,
	}
	if mode != 0 {
		argsMap["mode"] = mode
	}
	res, err := invokeRouted(ctx, opts, "upload", argsMap)
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
		printJSON(cmd.OutOrStdout(), map[string]any{
			"local":  localPath,
			"remote": remotePath,
			"size":   size,
			"sha256": sha,
		})
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "uploaded %d bytes to %s (sha256=%s)\n", size, remotePath, sha)
	return nil
}
