package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/searchproto"
)

// newGrepCmd searches file contents on the daemon via SearchHandler.
// Default behaviour treats <pattern> as a regex; pass --literal (or
// rely on the daemon's "regex" flag downstream) for fixed-string mode
// — kept implicit here because v0.0.2 sticks to the existing tool
// contract (regex=true by default). Path defaults to "." when omitted
// so the table form matches conventional grep usage.
func newGrepCmd() *cobra.Command {
	var glob string
	var ignoreCase bool
	var maxResults int
	cmd := &cobra.Command{
		Use:   "grep <pattern> [<path>]",
		Short: "Search file contents on the daemon",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			pattern := args[0]
			path := "."
			if len(args) == 2 {
				path = args[1]
			}
			return runGrep(cmd, pattern, path, glob, ignoreCase, maxResults, opts)
		},
	}
	cmd.Flags().StringVar(&glob, "glob", "", "optional **/*.go style file filter")
	cmd.Flags().BoolVar(&ignoreCase, "ignore-case", false, "case-insensitive match")
	cmd.Flags().IntVar(&maxResults, "max-results", 0, "cap total matches returned (0 = daemon default)")
	return cmd
}

// runGrep is the testable inner body. Tests inject opts.TLSConfig via
// (*phase4Env).newCLIOpts().
func runGrep(cmd *cobra.Command, pattern, path, glob string, ignoreCase bool, maxResults int, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	resolved, err := resolveArg(ctx, opts, path)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	path = resolved

	argsMap := map[string]any{
		"pattern":          pattern,
		"path":             path,
		"case_insensitive": ignoreCase,
	}
	if glob != "" {
		argsMap["glob"] = glob
	}
	if maxResults > 0 {
		argsMap["max_results"] = maxResults
	}
	res, err := invokeRouted(ctx, opts, "search", argsMap)
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

	matches, meta := extractGrepResults(rmap)

	if g.json {
		out := map[string]any{
			"pattern": pattern,
			"path":    path,
			"results": matches,
		}
		for k, v := range meta {
			out[k] = v
		}
		printJSON(cmd.OutOrStdout(), out)
		return nil
	}

	// Stable file:line:col: snippet layout. Sort by (file, line, column)
	// so two runs on the same fixture produce identical bytes.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].File != matches[j].File {
			return matches[i].File < matches[j].File
		}
		if matches[i].Line != matches[j].Line {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Column < matches[j].Column
	})
	for _, m := range matches {
		fmt.Fprintf(cmd.OutOrStdout(), "%s:%d:%d: %s\n", m.File, m.Line, m.Column, m.Snippet)
	}
	if len(matches) == 0 {
		if files, ok := meta["total_files_searched"].(int); ok {
			fmt.Fprintf(cmd.OutOrStdout(), "(no matches in %d files)\n", files)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "(no matches)")
		}
	}
	if truncated, _ := meta["truncated"].(bool); truncated {
		total, _ := meta["total_matched"].(int)
		fmt.Fprintf(cmd.OutOrStdout(), "--- showing %d of %d matches (truncated) ---\n", len(matches), total)
	}
	return nil
}

// extractGrepResults pulls the typed []SearchMatch + meta scalars out
// of the handler envelope. SearchHandler stores the slice directly
// alongside truncated / total counters under _meta.
func extractGrepResults(res map[string]any) ([]searchproto.SearchMatch, map[string]any) {
	meta, ok := res["_meta"].(map[string]any)
	if !ok {
		return nil, nil
	}
	out := map[string]any{
		"truncated":            meta["truncated"],
		"total_matched":        meta["total_matched"],
		"total_files_searched": meta["total_files_searched"],
		"duration_ms":          meta["duration_ms"],
	}
	switch v := meta["results"].(type) {
	case []searchproto.SearchMatch:
		return v, out
	case []any:
		// Defensive: handlers usually store the typed slice, but if a
		// future change marshals through JSON we still cope.
		var ms []searchproto.SearchMatch
		for _, e := range v {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			var sm searchproto.SearchMatch
			sm.File, _ = m["file"].(string)
			sm.Snippet, _ = m["snippet"].(string)
			if l, ok := m["line"].(float64); ok {
				sm.Line = int(l)
			}
			if c, ok := m["column"].(float64); ok {
				sm.Column = int(c)
			}
			ms = append(ms, sm)
		}
		return ms, out
	}
	return nil, out
}
