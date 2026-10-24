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

// ensureWorkingDir returns the CLI's working directory for opts.Target
// only, bootstrapping it from that daemon's advertised
// default_working_dir on first use and persisting it per-target in
// state.json. A non-empty stored value never triggers a network call,
// and no target ever inherits another target's directory.
func ensureWorkingDir(ctx context.Context, opts cliclient.Options) (string, error) {
	p := stateJSONPath(g.stateDir)
	st, err := state.LoadBridge(p)
	if err != nil {
		return "", err
	}
	if cwd := st.WorkingDirs[opts.Target]; cwd != "" {
		return cwd, nil
	}
	dwd, err := fetchDefaultWorkingDir(ctx, opts)
	if err != nil {
		return "", err
	}
	if dwd == "" {
		dwd = "/"
	}
	// Persist under the cross-process lock, re-checking inside: a
	// sibling CLI may have bootstrapped (or cd'd) this target while we
	// were on the network — its value wins over our fresh fetch.
	err = state.MutateBridge(p, func(st *state.BridgeState) error {
		if got := st.WorkingDirs[opts.Target]; got != "" {
			dwd = got
			return nil
		}
		if st.WorkingDirs == nil {
			st.WorkingDirs = map[string]string{}
		}
		st.WorkingDirs[opts.Target] = dwd
		return nil
	})
	if err != nil {
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

// setWorkingDir persists an explicit working directory for one target
// (used by `cd`). Other targets are untouched.
func setWorkingDir(target, dir string) error {
	p := stateJSONPath(g.stateDir)
	return state.MutateBridge(p, func(st *state.BridgeState) error {
		if st.WorkingDirs == nil {
			st.WorkingDirs = map[string]string{}
		}
		st.WorkingDirs[target] = dir
		return nil
	})
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
	// Second line of defence behind the gateway's pairing check: an old
	// gateway relays without version checks, and a pre-v0.0.5 daemon
	// advertises no default_working_dir — bootstrapping "/" from it would
	// silently mis-resolve every relative path. Fail loudly instead.
	if caps.ProtocolVersion < 2 {
		return "", errors.New("daemon speaks protocol v1 (agbridge <= v0.0.4, no working-directory support); upgrade the daemon")
	}
	return caps.DefaultWorkingDir, nil
}
