// Package sandbox provides daemon-side guardrails: non-root assertion and
// path allowlist matching used by tool implementations.
package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrRunningAsRoot is returned by RefuseRoot if EUID == 0.
var ErrRunningAsRoot = errors.New("sandbox: refusing to run as root")

// RefuseRoot returns ErrRunningAsRoot if the process is running as root.
// Used at daemon startup; documented as a hard precondition.
func RefuseRoot() error {
	if os.Geteuid() == 0 {
		return ErrRunningAsRoot
	}
	return nil
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
