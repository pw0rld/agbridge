package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/capabilities"
	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newCapsCmd asks the daemon for its current sandbox policy snapshot
// (allowed cwds, read/write paths, env allowlist, file-size cap, etc).
// Lets an AI agent introspect what it can attempt without burning an
// exec on a guess.
func newCapsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "caps",
		Short: "Show daemon sandbox capabilities",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			return runCaps(cmd, opts)
		},
	}
}

// runCaps is the testable inner body. Accepts the resolved opts so
// fixture tests can inject TLSConfig directly.
func runCaps(cmd *cobra.Command, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		return c.Router().CapabilitiesHandler(ctx, json.RawMessage(`{}`))
	})
	if err != nil {
		printErrorJSON(cmd.ErrOrStderr(), "network_lost", err.Error(), true)
		return err
	}
	resMap, ok := res.(map[string]any)
	if !ok {
		printErrorJSON(cmd.ErrOrStderr(), "protocol_error", "unexpected handler result shape", false)
		return fmt.Errorf("unexpected result shape: %T", res)
	}
	if isErr, _ := resMap["isError"].(bool); isErr {
		code, message := toolErrorFields(resMap)
		printErrorJSON(cmd.ErrOrStderr(), code, message, false)
		return fmt.Errorf("%s: %s", code, message)
	}
	caps, ok := extractCapabilities(resMap)
	if !ok {
		printErrorJSON(cmd.ErrOrStderr(), "protocol_error", "capabilities missing from response", false)
		return fmt.Errorf("capabilities missing from response")
	}
	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{
			"target":              opts.Target,
			"daemon_name":         caps.DaemonName,
			"daemon_hostname":     caps.DaemonHostname,
			"agbridge_version":    caps.AgbridgeVersion,
			"allowed_exec_cwds":   caps.AllowedExecCwds,
			"allowed_read_paths":  caps.AllowedReadPaths,
			"allowed_write_paths": caps.AllowedWritePaths,
			"env_allowlist":       caps.EnvAllowlist,
			"forbidden_ports":     caps.ForbiddenPorts,
			"max_file_bytes":      caps.MaxFileBytes,
		})
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "daemon_name:         %s\n", caps.DaemonName)
	fmt.Fprintf(cmd.OutOrStdout(), "daemon_hostname:     %s\n", caps.DaemonHostname)
	fmt.Fprintf(cmd.OutOrStdout(), "agbridge_version:    %s\n", caps.AgbridgeVersion)
	fmt.Fprintf(cmd.OutOrStdout(), "max_file_bytes:      %d\n", caps.MaxFileBytes)
	fmt.Fprintf(cmd.OutOrStdout(), "allowed_exec_cwds:   %v\n", caps.AllowedExecCwds)
	fmt.Fprintf(cmd.OutOrStdout(), "allowed_read_paths:  %v\n", caps.AllowedReadPaths)
	fmt.Fprintf(cmd.OutOrStdout(), "allowed_write_paths: %v\n", caps.AllowedWritePaths)
	fmt.Fprintf(cmd.OutOrStdout(), "env_allowlist:       %v\n", caps.EnvAllowlist)
	fmt.Fprintf(cmd.OutOrStdout(), "forbidden_ports:     %v\n", caps.ForbiddenPorts)
	return nil
}

// toolErrorFields pulls the canonical (error_code, message) pair out of
// an mcpErrorResult envelope. Returns ("", "") if the shape is unexpected.
func toolErrorFields(res map[string]any) (code, message string) {
	meta, _ := res["_meta"].(map[string]any)
	code, _ = meta["error_code"].(string)
	message, _ = meta["message"].(string)
	if message == "" {
		// Fall back to the first content text — handlers always emit one.
		if cs, ok := res["content"].([]any); ok && len(cs) > 0 {
			if m, ok := cs[0].(map[string]any); ok {
				message, _ = m["text"].(string)
			}
		}
	}
	return code, message
}

// extractCapabilities reads the typed capabilities.Capabilities value
// out of the handler result. CapabilitiesHandler stores the struct
// directly (not a map), so a simple type assertion is enough.
func extractCapabilities(res map[string]any) (capabilities.Capabilities, bool) {
	meta, ok := res["_meta"].(map[string]any)
	if !ok {
		return capabilities.Capabilities{}, false
	}
	caps, ok := meta["capabilities"].(capabilities.Capabilities)
	return caps, ok
}
