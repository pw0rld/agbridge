//go:build !unix

package filelock

import "os"

// No cross-process advisory lock available on this platform. The
// gateway is only supported on unix (Windows ships the CLI client
// only), so this build exists to keep the package compiling; the
// in-process mutex still serializes single-process access.
func tryLock(*os.File) (bool, error) { return true, nil }
func unlock(*os.File)                {}
