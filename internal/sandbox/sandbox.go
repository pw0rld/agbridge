// Package sandbox provides the daemon's one hard guardrail: a non-root
// assertion. Path scoping was removed in v0.0.4 — see THREAT-MODEL.md I6.
package sandbox

import (
	"errors"
	"os"
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
