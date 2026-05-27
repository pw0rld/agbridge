package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/cliclient"
)

// newExecCmd runs a command remotely on the daemon via ExecHandler.
// Use `--` to separate agbridge flags from the remote command and its
// own flags. Without `--`, the first positional is treated as the
// remote cmd and any subsequent positionals as its args (cobra still
// parses leading flags before the first positional).
//
// Default output: pass-through. Remote stdout → local stdout, remote
// stderr → local stderr. Local exit is always 0 when the call itself
// succeeded — the remote exit code is only surfaced via --json, so
// AI callers can branch on it without conflating transport failures
// with subprocess failures.
func newExecCmd() *cobra.Command {
	var cwd string
	var envArr []string
	var execTimeout int
	cmd := &cobra.Command{
		Use:   "exec [flags] -- <cmd> [args...]",
		Short: "Run a command on the daemon",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := loadOpts()
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "state_unreadable", err.Error(), false)
				return err
			}
			remote, err := splitExecArgs(cmd, args)
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "bad_payload", err.Error(), false)
				return err
			}
			envMap, err := parseEnvPairs(envArr)
			if err != nil {
				printErrorJSON(cmd.ErrOrStderr(), "bad_payload", err.Error(), false)
				return err
			}
			return runExec(cmd, remote, cwd, envMap, execTimeout, opts)
		},
	}
	cmd.Flags().StringVar(&cwd, "cwd", "", "daemon working directory")
	cmd.Flags().StringArrayVar(&envArr, "env", nil, "KEY=VALUE env var (repeatable)")
	cmd.Flags().IntVar(&execTimeout, "exec-timeout", 60000, "subprocess timeout in ms")
	return cmd
}

// splitExecArgs locates the `--` boundary and returns the remote
// command + its args. If `--` is absent, all positionals are treated
// as the remote command — this keeps `agbridge exec /bin/echo ok`
// working without forcing every caller to remember the separator.
func splitExecArgs(cmd *cobra.Command, args []string) ([]string, error) {
	sep := cmd.ArgsLenAtDash()
	var remote []string
	if sep < 0 {
		remote = args
	} else {
		remote = args[sep:]
	}
	if len(remote) == 0 {
		return nil, fmt.Errorf("missing remote command (use `agbridge exec -- <cmd> [args...]`)")
	}
	return remote, nil
}

// parseEnvPairs turns the repeated --env KEY=VALUE flag values into a
// map. Each entry must contain at least one '=' separator; empty
// values are allowed (KEY=), but bare KEY is rejected so the user
// doesn't accidentally leak host env on the daemon side.
func parseEnvPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		idx := strings.IndexByte(p, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("--env %q: expected KEY=VALUE", p)
		}
		out[p[:idx]] = p[idx+1:]
	}
	return out, nil
}

// runExec is the testable inner body. Tests inject opts.TLSConfig via
// env.newCLIOpts().
func runExec(cmd *cobra.Command, remote []string, cwd string, env map[string]string, timeoutMs int, opts cliclient.Options) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
	defer cancel()

	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		argsMap := map[string]any{
			"cmd":        remote[0],
			"args":       remote[1:],
			"timeout_ms": timeoutMs,
		}
		if cwd != "" {
			argsMap["cwd"] = cwd
		}
		if len(env) > 0 {
			argsMap["env"] = env
		}
		argsJSON, _ := json.Marshal(argsMap)
		return c.Router().ExecHandler(ctx, argsJSON)
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

	stdout, stderr, exitcode, durationMs := extractExecResult(rmap)

	if g.json {
		printJSON(cmd.OutOrStdout(), map[string]any{
			"stdout":      stdout,
			"stderr":      stderr,
			"exitcode":    exitcode,
			"duration_ms": durationMs,
		})
		return nil
	}
	// Pass-through: stdout → cmd stdout, stderr → cmd stderr. We do
	// NOT propagate the remote exitcode into the local exit status —
	// callers that care should use --json.
	if stdout != "" {
		_, _ = cmd.OutOrStdout().Write([]byte(stdout))
	}
	if stderr != "" {
		_, _ = cmd.ErrOrStderr().Write([]byte(stderr))
	}
	return nil
}

// extractExecResult pulls stdout/stderr (decoding _b64 fallbacks) plus
// the exit metadata out of the ExecHandler envelope. Matches the F9
// shape: text-only when UTF-8, base64 fallback when not.
func extractExecResult(rmap map[string]any) (stdout, stderr string, exitcode, durationMs int64) {
	meta, _ := rmap["_meta"].(map[string]any)
	if meta == nil {
		return "", "", 0, 0
	}
	if s, ok := meta["stdout"].(string); ok {
		stdout = s
	} else if b, ok := meta["stdout_b64"].(string); ok {
		if dec, err := base64.StdEncoding.DecodeString(b); err == nil {
			stdout = string(dec)
		}
	}
	if s, ok := meta["stderr"].(string); ok {
		stderr = s
	} else if b, ok := meta["stderr_b64"].(string); ok {
		if dec, err := base64.StdEncoding.DecodeString(b); err == nil {
			stderr = string(dec)
		}
	}
	if v, ok := meta["exitcode"].(int64); ok {
		exitcode = v
	} else if v, ok := meta["exitcode"].(float64); ok {
		exitcode = int64(v)
	} else if v, ok := meta["exitcode"].(int); ok {
		exitcode = int64(v)
	}
	if v, ok := meta["duration_ms"].(int64); ok {
		durationMs = v
	} else if v, ok := meta["duration_ms"].(float64); ok {
		durationMs = int64(v)
	} else if v, ok := meta["duration_ms"].(int); ok {
		durationMs = int64(v)
	}
	return stdout, stderr, exitcode, durationMs
}
