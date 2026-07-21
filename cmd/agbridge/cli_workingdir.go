package main

import (
	"context"
	"encoding/json"
	"errors"
	"path"

	"github.com/pw0rld/agbridge/internal/capabilities"
	"github.com/pw0rld/agbridge/internal/cliclient"
	"github.com/pw0rld/agbridge/internal/state"
)

// resolveRemotePath maps a user-supplied path argument onto the daemon
// filesystem, relative to the CLI's working directory. This is a soft
// base, not a fence: absolute paths and ".." pass straight through.
// Remote paths are POSIX, so we use path (not filepath).
func resolveRemotePath(cwd, arg string) string {
	if arg == "" {
		return cwd
	}
	if path.IsAbs(arg) {
		return path.Clean(arg)
	}
	return path.Clean(path.Join(cwd, arg))
}

// ensureWorkingDir returns the CLI's working directory, bootstrapping it
// from the daemon's advertised default_working_dir on first use and
// persisting it to state.json. A non-empty stored value never triggers a
// network call.
func ensureWorkingDir(ctx context.Context, opts cliclient.Options) (string, error) {
	p := stateJSONPath(g.stateDir)
	st, err := state.LoadBridge(p)
	if err != nil {
		return "", err
	}
	if st.WorkingDir != "" {
		return st.WorkingDir, nil
	}
	dwd, err := fetchDefaultWorkingDir(ctx, opts)
	if err != nil {
		return "", err
	}
	if dwd == "" {
		dwd = "/"
	}
	st.WorkingDir = dwd
	if err := state.SaveBridge(p, st); err != nil {
		return "", err
	}
	return dwd, nil
}

// resolveArg resolves a user-supplied path argument against the working
// directory. Absolute args need no cwd, so it skips the daemon round-trip
// (and the state.json dependency) entirely; relative or empty args fetch
// cwd via ensureWorkingDir first.
func resolveArg(ctx context.Context, opts cliclient.Options, arg string) (string, error) {
	if arg != "" && path.IsAbs(arg) {
		return path.Clean(arg), nil
	}
	cwd, err := ensureWorkingDir(ctx, opts)
	if err != nil {
		return "", err
	}
	return resolveRemotePath(cwd, arg), nil
}

// setWorkingDir persists an explicit working directory (used by `cd`).
func setWorkingDir(dir string) error {
	p := stateJSONPath(g.stateDir)
	st, err := state.LoadBridge(p)
	if err != nil {
		return err
	}
	st.WorkingDir = dir
	return state.SaveBridge(p, st)
}

// fetchDefaultWorkingDir does one capabilities round-trip and returns the
// daemon's advertised default_working_dir.
func fetchDefaultWorkingDir(ctx context.Context, opts cliclient.Options) (string, error) {
	res, err := cliclient.Invoke(ctx, opts, func(ctx context.Context, c *cliclient.Client) (any, error) {
		return c.Router().CapabilitiesHandler(ctx, json.RawMessage("{}"))
	})
	if err != nil {
		return "", err
	}
	rmap, ok := res.(map[string]any)
	if !ok {
		return "", errors.New("capabilities: unexpected shape")
	}
	meta, _ := rmap["_meta"].(map[string]any)
	caps, ok := meta["capabilities"].(capabilities.Capabilities)
	if !ok {
		return "", errors.New("capabilities: missing snapshot")
	}
	return caps.DefaultWorkingDir, nil
}
