package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newEditCmd performs a literal old→new substitution on a file on the
// daemon via EditFileHandler. Atomic: the daemon backs up the original
// to a sibling .bak before swapping in the rewritten file. By default
// --old must occur exactly once; pass --replace-all to swap every
// occurrence.
func newEditCmd() *cobra.Command {
	var oldStr string
	var newStr string
	var replaceAll bool
	cmd := &cobra.Command{
		Use:   "edit <path>",
		Short: "Replace old_string with new_string in a file on the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			if oldStr == "" {
				err := fmt.Errorf("--old is required and must not be empty")
				printErrorJSON(cmd.ErrOrStderr(), "bad_payload", err.Error(), false)
				return err
			}
			return runEdit(cmd, args[0], oldStr, newStr, replaceAll, opts)
		},
	}
	cmd.Flags().StringVar(&oldStr, "old", "", "literal string to replace (required, non-empty)")
	cmd.Flags().StringVar(&newStr, "new", "", "replacement string (empty = delete)")
	cmd.Flags().BoolVar(&replaceAll, "replace-all", false, "replace every occurrence (default requires exactly one match)")
	return cmd
}

// runEdit is the testable inner body.
func runEdit(cmd *cobra.Command, path, oldStr, newStr string, replaceAll bool, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	resolved, err := resolveArg(ctx, opts, path)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	path = resolved

	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		argsJSON, _ := json.Marshal(map[string]any{
			"path":        path,
			"old_string":  oldStr,
			"new_string":  newStr,
			"replace_all": replaceAll,
		})
		return c.Router().EditFileHandler(ctx, argsJSON)
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
	backup, _ := meta["backup_path"].(string)

	if g.json {
		out := map[string]any{
			"path":   path,
			"size":   size,
			"sha256": sha,
		}
		if backup != "" {
			out["backup_path"] = backup
		}
		printJSON(cmd.OutOrStdout(), out)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "edited %d bytes in %s (sha256=%s)\n", size, path, sha)
	if backup != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "backup: %s\n", backup)
	}
	return nil
}
