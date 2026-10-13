package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newCatCmd reads a file on the daemon via ReadFileHandler and writes
// its bytes to stdout. Mirrors `cat`. JSON mode emits the structured
// {path, content, sha256, size} envelope instead.
func newCatCmd() *cobra.Command {
	var offset int64
	var limit int64
	cmd := &cobra.Command{
		Use:   "cat <path>",
		Short: "Read a file from the daemon to stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runCat(cmd, args[0], offset, limit, opts)
		},
	}
	cmd.Flags().Int64Var(&offset, "offset", 0, "skip the first N bytes")
	cmd.Flags().Int64Var(&limit, "limit", 0, "stop after N bytes (0 = unlimited up to daemon cap)")
	return cmd
}

// runCat is the testable inner body. Tests inject opts.TLSConfig via
// env.newCLIOpts() to bypass the production CertPin path.
func runCat(cmd *cobra.Command, path string, offset, limit int64, opts cliclient.Options) error {
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
			"path":   path,
			"offset": offset,
			"limit":  limit,
		})
		return c.Router().ReadFileHandler(ctx, argsJSON)
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

	// Extract content. Handler returns text in content[0].text when the
	// file is UTF-8, and _meta.content_b64 (with a placeholder text)
	// when the file is binary. See buildReadFileResult in
	// internal/cliclient/handlers_file.go.
	meta, _ := rmap["_meta"].(map[string]any)
	var size int64
	if s, ok := meta["size"].(int64); ok {
		size = s
	} else if s, ok := meta["size"].(float64); ok {
		size = int64(s)
	}
	sha, _ := meta["sha256"].(string)

	var data []byte
	if b64Str, ok := meta["content_b64"].(string); ok && b64Str != "" {
		decoded, dErr := base64.StdEncoding.DecodeString(b64Str)
		if dErr != nil {
			printErrorJSON(cmd.ErrOrStderr(), "bad_payload", "decode content_b64: "+dErr.Error(), false)
			return dErr
		}
		data = decoded
	} else {
		// UTF-8 path: handler embedded raw file text in content[0].text.
		if cs, ok := rmap["content"].([]any); ok && len(cs) > 0 {
			if m, ok := cs[0].(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					data = []byte(s)
				}
			}
		}
	}

	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{
			"path":    path,
			"size":    size,
			"sha256":  sha,
			"content": string(data),
		})
		return nil
	}
	// Default: raw bytes. No trailing newline injected — `cat` semantics.
	if _, err := cmd.OutOrStdout().Write(data); err != nil {
		return err
	}
	return nil
}
