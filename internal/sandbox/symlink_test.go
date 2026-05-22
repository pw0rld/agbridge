package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests pin the zero-trust invariant that an attacker with write
// access to a directory inside the allowlist cannot escape it via a
// planted symlink. They are deliberately written against the
// post-fix PathAllowedResolved API — they MUST exist before the fix
// lands so the fix is provably tied to the regression it closes.

func TestPathAllowedResolvedRejectsSymlinkEscape(t *testing.T) {
	// Set up: allowed dir contains a symlink pointing outside.
	allowed := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret")
	if err := os.WriteFile(target, []byte("not for you"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	allowlist := []string{allowed + "/*"}

	// Lexical check passes (link looks like it's inside `allowed`).
	if !PathAllowed(link, allowlist) {
		t.Fatal("setup invalid: lexical PathAllowed should accept the link path itself")
	}
	// But the resolved path escapes. The fix must reject.
	if _, ok := PathAllowedResolved(link, allowlist); ok {
		t.Errorf("PathAllowedResolved must reject symlink escape; allowed=%s link=%s target=%s",
			allowed, link, target)
	}
}

func TestPathAllowedResolvedRejectsAncestorSymlink(t *testing.T) {
	// Set up: an *ancestor* of the requested path is a symlink that
	// escapes. The file under it doesn't exist yet (write-file case):
	// allowed/sub/file, where allowed/sub -> /etc.
	allowed := t.TempDir()
	link := filepath.Join(allowed, "sub")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(link, "passwd-clone") // /etc/passwd-clone (non-existent tail)

	allowlist := []string{allowed + "/*"}

	if _, ok := PathAllowedResolved(target, allowlist); ok {
		t.Errorf("PathAllowedResolved must reject ancestor-symlink escape; target=%s", target)
	}
}

func TestPathAllowedResolvedAcceptsInsideSymlink(t *testing.T) {
	// Symlink that stays inside the allowlist should still pass —
	// users legitimately use symlinks inside their workspace.
	allowed := t.TempDir()
	real := filepath.Join(allowed, "real-file")
	if err := os.WriteFile(real, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	allowlist := []string{allowed + "/*"}

	got, ok := PathAllowedResolved(link, allowlist)
	if !ok {
		t.Fatalf("inside-allowlist symlink must be accepted; link=%s", link)
	}
	// Returned path should be the resolved real path so callers can
	// log/operate on the canonical name.
	if want, _ := filepath.EvalSymlinks(real); got != want {
		t.Errorf("resolved path: got %s, want %s", got, want)
	}
}

func TestPathAllowedResolvedHandlesNonExistentLeaf(t *testing.T) {
	// Writefile case: leaf doesn't exist yet but everything above
	// is real and inside the allowlist. Must accept.
	allowed := t.TempDir()
	leaf := filepath.Join(allowed, "new-file") // doesn't exist
	allowlist := []string{allowed + "/*"}

	got, ok := PathAllowedResolved(leaf, allowlist)
	if !ok {
		t.Fatalf("non-existent leaf in allowed dir must be accepted; leaf=%s", leaf)
	}
	// The leaf's resolved form is the resolved parent + base.
	wantParent, _ := filepath.EvalSymlinks(allowed)
	want := filepath.Join(wantParent, "new-file")
	if got != want {
		t.Errorf("resolved leaf: got %s, want %s", got, want)
	}
}

func TestPathAllowedResolvedPreservesDotDotRejection(t *testing.T) {
	// Pre-existing semantic: .. traversal must reject. The resolver
	// must not regress this.
	allowed := t.TempDir()
	parent := filepath.Dir(allowed)
	outside := filepath.Join(parent, "sibling")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}

	// /allowed/../sibling/file — lexically escapes via ..
	attack := filepath.Join(allowed, "..", filepath.Base(outside), "file")
	allowlist := []string{allowed + "/*"}

	if _, ok := PathAllowedResolved(attack, allowlist); ok {
		t.Errorf(".. traversal must remain rejected; attack=%s", attack)
	}
}

func TestPathAllowedResolvedRejectsRelativePath(t *testing.T) {
	// Defensive: PathAllowedResolved must only accept absolute paths,
	// matching the lexical PathAllowed contract.
	if _, ok := PathAllowedResolved("relative/path", []string{"/allowed/*"}); ok {
		t.Errorf("relative path must be rejected")
	}
}

func TestPathAllowedResolvedEmptyAllowlist(t *testing.T) {
	if _, ok := PathAllowedResolved("/anything", nil); ok {
		t.Errorf("empty allowlist must deny")
	}
}
