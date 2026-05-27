package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/fsproto"
)

// lsWalk is one (path, entries) pair captured during an `agbridge ls`
// invocation. A single non-recursive call produces exactly one walk;
// --recursive emits one per visited directory in BFS order.
type lsWalk struct {
	Path    string             `json:"path"`
	Entries []fsproto.DirEntry `json:"entries"`
}

// newLsCmd lists a directory on the daemon via ListDirectoryHandler.
// Non-recursive by default. --recursive expands children client-side via
// repeated round-trips; --depth caps that walk (0 = unlimited).
//
// Recursion lives in the CLI rather than the daemon protocol because
// ListDirRequest is intentionally single-level: keeping the daemon dumb
// avoids a second permission surface and lets the AI control depth from
// the same place it controls the entry point.
func newLsCmd() *cobra.Command {
	var recursive bool
	var depth int
	cmd := &cobra.Command{
		Use:   "ls <path>",
		Short: "List directory entries on the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runLs(cmd, args[0], recursive, depth, opts)
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "R", false, "descend into subdirectories (client-side walk)")
	cmd.Flags().IntVar(&depth, "depth", 0, "max recursion depth when --recursive (0 = unlimited)")
	return cmd
}

// runLs is the testable inner body. Tests inject opts.TLSConfig via
// (*phase4Env).newCLIOpts() instead of going through loadOpts.
func runLs(cmd *cobra.Command, root string, recursive bool, depth int, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	var walks []lsWalk
	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		queue := []string{root}
		seenDepth := map[string]int{root: 0}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			argsJSON, _ := json.Marshal(map[string]any{"path": cur})
			r, err := c.Router().ListDirectoryHandler(ctx, argsJSON)
			if err != nil {
				return nil, err
			}
			rmap, ok := r.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("ls: unexpected handler shape: %T", r)
			}
			if isErr, _ := rmap["isError"].(bool); isErr {
				// Surface the first failing leaf and stop; partial results
				// would muddle the error contract.
				return rmap, nil
			}
			meta, _ := rmap["_meta"].(map[string]any)
			entries, _ := meta["entries"].([]fsproto.DirEntry)
			walks = append(walks, lsWalk{Path: cur, Entries: entries})
			if !recursive {
				continue
			}
			curDepth := seenDepth[cur]
			if depth > 0 && curDepth >= depth {
				continue
			}
			for _, e := range entries {
				if e.Type != "dir" {
					continue
				}
				sub := path.Join(cur, e.Name)
				if _, dup := seenDepth[sub]; dup {
					continue
				}
				seenDepth[sub] = curDepth + 1
				queue = append(queue, sub)
			}
		}
		return nil, nil
	})
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "network_lost", err.Error(), true)
		return err
	}
	if errMap, ok := res.(map[string]any); ok && errMap != nil {
		code, message := toolErrorFields(errMap)
		printErrorJSON(cmd.ErrOrStderr(), code, message, false)
		return fmt.Errorf("%s: %s", code, message)
	}

	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{
			"root":      root,
			"recursive": recursive,
			"depth":     depth,
			"walks":     walks,
		})
		return nil
	}
	return renderWalks(cmd.OutOrStdout(), walks)
}

func renderWalks(w io.Writer, walks []lsWalk) error {
	for i, wk := range walks {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if len(walks) > 1 {
			fmt.Fprintf(w, "%s:\n", wk.Path)
		}
		sort.SliceStable(wk.Entries, func(i, j int) bool { return wk.Entries[i].Name < wk.Entries[j].Name })
		for _, e := range wk.Entries {
			fmt.Fprintf(w, "%-4s %10d %o %s\n", e.Type, e.Size, e.Mode, e.Name)
		}
		if len(wk.Entries) == 0 {
			fmt.Fprintln(w, "(empty directory)")
		}
	}
	return nil
}
