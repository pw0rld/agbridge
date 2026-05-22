// Package sandbox provides daemon-side guardrails: non-root assertion and
// path allowlist matching used by tool implementations.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrRunningAsRoot is returned by RefuseRoot if EUID == 0.
var ErrRunningAsRoot = errors.New("sandbox: refusing to run as root")

// RefuseRoot returns ErrRunningAsRoot if the process is running as root.
// Used at daemon startup; documented as a hard precondition.
//
// AGBRIDGE_TEST_ALLOW_ROOT=1 bypasses the check. This exists solely for
// binary integration tests in environments where dropping privileges
// (chmod-ing every parent dir + SysProcAttr.Credential) is impractical.
// NEVER set this in production: the daemon's threat model assumes a
// non-root EUID for the exec / file / port_forward tools.
func RefuseRoot() error {
	if os.Geteuid() == 0 {
		if os.Getenv("AGBRIDGE_TEST_ALLOW_ROOT") == "1" {
			return nil
		}
		return ErrRunningAsRoot
	}
	return nil
}

// FormatAllowlist renders an allowlist as a short human-readable string
// suitable for embedding in error messages. Empty input returns "<empty>".
func FormatAllowlist(allowlist []string) string {
	if len(allowlist) == 0 {
		return "<empty>"
	}
	return "[" + strings.Join(allowlist, ", ") + "]"
}

// PathAllowed checks whether path is under any of the prefix-glob entries
// in allowlist. The path is canonicalized (filepath.Clean) first so that
// `..` traversal cannot escape an allowed root.
//
// Glob format: a trailing "/*" means "any descendant of this directory".
// Plain entries (no trailing /*) require an exact match.
func PathAllowed(path string, allowlist []string) bool {
	if path == "" || len(allowlist) == 0 {
		return false
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return false
	}
	for _, g := range allowlist {
		if strings.HasSuffix(g, "/*") {
			root := strings.TrimSuffix(g, "/*")
			if strings.HasPrefix(clean, root+"/") {
				return true
			}
		} else {
			if clean == g {
				return true
			}
		}
	}
	return false
}

// PathAllowedResolved is PathAllowed plus a real-filesystem symlink check.
// In addition to the lexical guarantees (clean, abs, prefix match), it
// resolves any symlinks in the existing ancestor chain and re-applies
// the allowlist check to the resolved path. This closes the
// allowlist-escape-via-symlink hole that PathAllowed alone leaves open:
// a writable subdirectory inside the allowlist could otherwise be used
// to plant a symlink to /etc/passwd and bypass the prefix check.
//
// Returns the resolved absolute path (suitable for the actual
// filesystem op) and true when allowed, ("", false) otherwise.
//
// Caveat: not TOCTOU-safe. A symlink planted between this check and
// the eventual open() will still win. For Linux callers that need
// stronger guarantees, switch to openat2 with RESOLVE_NO_SYMLINKS — out
// of scope for this layer. The daemon's threat model (non-root EUID,
// operator-controlled allowlist roots, no remote write to allowlist
// parent dirs) makes the residual TOCTOU window acceptable.
func PathAllowedResolved(path string, allowlist []string) (string, bool) {
	if !PathAllowed(path, allowlist) {
		return "", false
	}
	clean := filepath.Clean(path)
	resolved, err := resolveExistingAncestor(clean)
	if err != nil {
		// Couldn't walk up to find any existing ancestor — should be
		// impossible on a real filesystem (root always exists). Fail
		// closed.
		return "", false
	}
	if !PathAllowed(resolved, allowlist) {
		return "", false
	}
	return resolved, true
}

// resolveExistingAncestor walks `clean` upward to find the deepest
// existing ancestor, evaluates its symlinks, then re-joins the
// non-existent tail. This lets us check writefile-style paths
// (where the leaf doesn't exist yet) without losing symlink
// resolution on the parent chain.
func resolveExistingAncestor(clean string) (string, error) {
	cur := clean
	var tail []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor for %q", clean)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	if len(tail) == 0 {
		return real, nil
	}
	return filepath.Join(append([]string{real}, tail...)...), nil
}
