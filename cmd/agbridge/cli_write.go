package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newWriteCmd writes a file on the daemon via WriteFileHandler.
//
// Content sources, in priority order:
//   - --content "<string>"        inline literal
//   - --from-file <local-path>    read from local disk
//   - (default)                   read from stdin
//
// The handler accepts either `content` (raw string) or `content_b64`
// (base64 for arbitrary bytes). We pick based on whether the bytes are
// valid UTF-8 — same heuristic as exec / read_file so token usage stays
// proportional to actual binary content.
func newWriteCmd() *cobra.Command {
	var content string
	var fromFile string
	var modeStr string
	var explicit bool
	cmd := &cobra.Command{
		Use:   "write <path>",
		Short: "Write a file on the daemon",
		Args:  cobra.ExactArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			// Track whether --content was given (even as empty string)
			// vs not given at all. Empty string is a valid zero-byte write.
			explicit = cmd.Flags().Changed("content")
		},
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
			data, err := resolveWriteContent(cmd, content, explicit, fromFile)
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "bad_payload", err.Error(), false)
				return err
			}
			return runWrite(cmd, args[0], data, mode, opts)
		},
	}
	cmd.Flags().StringVar(&content, "content", "", "inline content (use empty string to touch a zero-byte file)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read content from a local file path")
	cmd.Flags().StringVar(&modeStr, "mode", "", "file mode (octal, e.g. 0644); 0 = daemon default")
	return cmd
}

// resolveWriteContent picks the content source per the priority order
// documented on newWriteCmd. Returns the raw bytes; the encoding
// (string vs base64) is decided later in runWrite.
func resolveWriteContent(cmd *cobra.Command, content string, explicit bool, fromFile string) ([]byte, error) {
	if explicit && fromFile != "" {
		return nil, fmt.Errorf("--content and --from-file are mutually exclusive")
	}
	if explicit {
		return []byte(content), nil
	}
	if fromFile != "" {
		return os.ReadFile(fromFile)
	}
	// Default: read stdin. cmd.InOrStdin() lets tests inject a buffer.
	return io.ReadAll(cmd.InOrStdin())
}

// runWrite is the testable inner body.
func runWrite(cmd *cobra.Command, path string, data []byte, mode uint32, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	resolved, err := resolveArg(ctx, opts, path)
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "working_dir_unavailable", err.Error(), true)
		return err
	}
	path = resolved

	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		argsMap := map[string]any{"path": path}
		if mode != 0 {
			argsMap["mode"] = mode
		}
		// Prefer the raw `content` field for valid UTF-8 (cheaper on
		// the wire and matches the F9 write_file ergonomics). Fall
		// back to base64 for arbitrary bytes.
		if utf8.Valid(data) {
			argsMap["content"] = string(data)
		} else {
			argsMap["content_b64"] = base64.StdEncoding.EncodeToString(data)
		}
		argsJSON, _ := json.Marshal(argsMap)
		return c.Router().WriteFileHandler(ctx, argsJSON)
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
		printJSON(cmd.OutOrStdout(), map[string]any{
			"path":          path,
			"bytes_written": size,
			"sha256":        sha,
		})
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %d bytes to %s (sha256=%s)\n", size, path, sha)
	return nil
}

