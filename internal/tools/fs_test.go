package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pw0rld/agbridge/internal/fsproto"
)

func TestListDirectoryHappyPath(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644)
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "exec.sh"), []byte("#!/bin/sh"), 0o755)

	resp := ListDirectory(fsproto.ListDirRequest{Path: dir})
	if resp.Err != "" {
		t.Fatalf("err: %q (%s)", resp.Err, resp.ErrDetail)
	}
	byName := map[string]fsproto.DirEntry{}
	for _, e := range resp.Entries {
		byName[e.Name] = e
	}
	if len(byName) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(byName), resp.Entries)
	}
	if byName["a.txt"].Type != "file" || byName["a.txt"].Size != 2 {
		t.Errorf("a.txt entry wrong: %+v", byName["a.txt"])
	}
	if byName["sub"].Type != "dir" {
		t.Errorf("sub entry wrong: %+v", byName["sub"])
	}
	if byName["exec.sh"].Mode&0o100 == 0 {
		t.Errorf("exec.sh should have exec bit: %o", byName["exec.sh"].Mode)
	}
}

func TestListDirectoryNotFound(t *testing.T) {
	dir := t.TempDir()
	resp := ListDirectory(fsproto.ListDirRequest{Path: filepath.Join(dir, "nope")})
	if resp.Err != "not_found" {
		t.Errorf("err: %q", resp.Err)
	}
}

func TestListDirectoryOnFileNotDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	resp := ListDirectory(fsproto.ListDirRequest{Path: path})
	if resp.Err != "not_a_directory" {
		t.Errorf("err: %q (%s)", resp.Err, resp.ErrDetail)
	}
}

func TestStatHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "z.bin")
	_ = os.WriteFile(path, []byte("hello world"), 0o600)
	resp := Stat(fsproto.StatRequest{Path: path})
	if resp.Err != "" {
		t.Fatalf("err: %q (%s)", resp.Err, resp.ErrDetail)
	}
	if resp.Entry == nil {
		t.Fatal("entry nil")
	}
	if resp.Entry.Type != "file" || resp.Entry.Size != 11 {
		t.Errorf("entry: %+v", resp.Entry)
	}
	if resp.Entry.Mode != 0o600 {
		t.Errorf("mode: %o", resp.Entry.Mode)
	}
}

func TestStatNotFound(t *testing.T) {
	dir := t.TempDir()
	resp := Stat(fsproto.StatRequest{Path: filepath.Join(dir, "no")})
	if resp.Err != "not_found" || resp.Entry != nil {
		t.Errorf("got err=%q entry=%v", resp.Err, resp.Entry)
	}
}
