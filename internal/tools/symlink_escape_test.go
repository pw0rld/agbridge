package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/searchproto"
)

var _ = context.Background // keep import for ReadFile/WriteFile signatures below

// These are end-to-end regression tests for invariant #6 — the
// symlink-escape gap closed by PathAllowedResolved. They run every
// handler that takes a path through a malicious symlink and assert
// the handler returns "path_forbidden". The intent is to fail
// loudly if anyone refactors a handler back to the lexical
// PathAllowed check.

func setupSymlinkEscape(t *testing.T) (allowed, outsideTarget, linkInside string) {
	t.Helper()
	allowed = t.TempDir()
	outside := t.TempDir()
	outsideTarget = filepath.Join(outside, "secret")
	if err := os.WriteFile(outsideTarget, []byte("not for you"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkInside = filepath.Join(allowed, "trap")
	if err := os.Symlink(outsideTarget, linkInside); err != nil {
		t.Fatal(err)
	}
	return
}

func TestReadFileRejectsSymlinkEscape(t *testing.T) {
	allowed, _, link := setupSymlinkEscape(t)
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: link},
		[]string{allowed + "/*"},
		1<<20,
		func(c fileproto.FileChunk) {},
	)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "path_forbidden" {
		t.Errorf("symlink escape via ReadFile: got Err=%q, want path_forbidden", complete.Err)
	}
}

func TestWriteFileRejectsSymlinkEscape(t *testing.T) {
	// Write target is the symlink itself; PathAllowedResolved must
	// resolve and reject before the temp-file dance starts.
	allowed, _, link := setupSymlinkEscape(t)
	called := false
	complete, err := WriteFile(context.Background(),
		fileproto.FileWriteRequest{Path: link},
		[]string{allowed + "/*"},
		1<<20,
		func() (fileproto.FileChunk, error) {
			called = true
			return fileproto.FileChunk{Data: []byte("oops"), Eof: true}, nil
		},
	)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if complete.Err != "path_forbidden" {
		t.Errorf("symlink escape via WriteFile: got Err=%q, want path_forbidden", complete.Err)
	}
	if called {
		t.Errorf("chunk source called after path rejection — handler should bail before consuming chunks")
	}
}

func TestListDirRejectsSymlinkEscapeViaParent(t *testing.T) {
	// Ancestor-symlink case: allowed/sub -> /etc, then list
	// allowed/sub. Lexical check would pass; PathAllowedResolved
	// must reject because the resolved path leaves the allowlist.
	allowed := t.TempDir()
	link := filepath.Join(allowed, "sub")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	resp := ListDirectory(fsproto.ListDirRequest{Path: link}, []string{allowed + "/*"})
	if resp.Err != "path_forbidden" {
		t.Errorf("symlink escape via ListDirectory: got Err=%q, want path_forbidden", resp.Err)
	}
}

func TestStatRejectsSymlinkEscape(t *testing.T) {
	allowed, _, link := setupSymlinkEscape(t)
	resp := Stat(fsproto.StatRequest{Path: link}, []string{allowed + "/*"})
	if resp.Err != "path_forbidden" {
		t.Errorf("symlink escape via Stat: got Err=%q, want path_forbidden", resp.Err)
	}
}

func TestSearchRejectsSymlinkEscape(t *testing.T) {
	// Search starts at req.Path. If we let an attacker point that at
	// a symlink escape, we'd grep through whatever's outside the
	// allowlist. Must reject.
	allowed := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "loot"), []byte("password=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	resp := Search(searchproto.SearchRequest{
		Path:    link,
		Pattern: "password",
	}, []string{allowed + "/*"})
	if resp.Err != "path_forbidden" {
		t.Errorf("symlink escape via Search: got Err=%q, want path_forbidden", resp.Err)
	}
}
