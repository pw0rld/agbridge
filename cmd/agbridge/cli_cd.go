package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/fsproto"
)

// newPwdCmd prints the CLI's current working directory (bootstrapping it
// from the daemon's advertised default on first use).
func newPwdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pwd",
		Short: "Print the current working directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runPwd(cmd, opts)
		},
	}
}

// runPwd is the testable inner body.
func runPwd(cmd *cobra.Command, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()
	cwd, err := ensureWorkingDir(ctx, opts)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{"working_dir": cwd})
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), cwd)
	return nil
}

// newCdCmd sets the CLI working directory after stat-validating that the
// target exists on the daemon and is a directory. No fence: absolute and
// ".." targets are permitted.
func newCdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cd <path>",
		Short: "Change the working directory (validated against the daemon)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runCd(cmd, args[0], opts)
		},
	}
}

// runCd is the testable inner body.
func runCd(cmd *cobra.Command, arg string, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()
	cwd, err := ensureWorkingDir(ctx, opts)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	target := resolveRemotePath(cwd, arg)
	if err := statIsDir(ctx, opts, target); err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "not_a_directory", err.Error(), false)
		return err
	}
	if err := setWorkingDir(target); err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "state_unwritable", err.Error(), false)
		return err
	}
	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{"working_dir": target})
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), target)
	return nil
}

// statIsDir stats target on the daemon and returns nil iff it is a directory.
func statIsDir(ctx context.Context, opts cliclient.Options, target string) error {
	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		argsJSON, _ := json.Marshal(map[string]any{"path": target})
		return c.Router().StatHandler(ctx, argsJSON)
	})
	if err != nil {
		return err
	}
	rmap, _ := res.(map[string]any)
	if isErr, _ := rmap["isError"].(bool); isErr {
		code, message := toolErrorFields(rmap)
		return fmt.Errorf("%s: %s", code, message)
	}
	meta, _ := rmap["_meta"].(map[string]any)
	entry, ok := meta["entry"].(*fsproto.DirEntry)
	if !ok || entry == nil {
		return fmt.Errorf("stat %s: no entry returned", target)
	}
	if entry.Type != "dir" {
		return fmt.Errorf("%s is a %s, not a directory", target, entry.Type)
	}
	return nil
}
