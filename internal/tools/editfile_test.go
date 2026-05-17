package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/fileproto"
)

func TestEditFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	_ = os.WriteFile(path, []byte("package x\n\nfunc Old() {}\n"), 0o644)
	allow := []string{dir + "/*"}

	complete, err := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "func Old()", NewString: "func New()"},
		allow, 1<<20)
	if err != nil {
		t.Fatalf("EditFile: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("unexpected err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "package x\n\nfunc New() {}\n" {
		t.Errorf("content after edit:\n%s", got)
	}
}

func TestEditFileNotFound(t *testing.T) {
	dir := t.TempDir()
	allow := []string{dir + "/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: filepath.Join(dir, "nope.txt"), OldString: "a", NewString: "b"},
		allow, 1<<20)
	if complete.Err != "not_found" {
		t.Errorf("expected not_found, got %q", complete.Err)
	}
}

func TestEditFilePathForbidden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	allowlist := []string{"/different/a", "/different/b/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "x", NewString: "y"},
		allowlist, 1<<20)
	if complete.Err != "path_forbidden" {
		t.Errorf("err: %q", complete.Err)
	}
	if complete.ErrDetail == "" {
		t.Fatal("ErrDetail required")
	}
	for _, want := range allowlist {
		if !strings.Contains(complete.ErrDetail, want) {
			t.Errorf("ErrDetail missing %q: %s", want, complete.ErrDetail)
		}
	}
}

func TestEditFileOldStringNotPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("hello world"), 0o644)
	allow := []string{dir + "/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "absent", NewString: "x"},
		allow, 1<<20)
	if complete.Err != "old_string_not_found" {
		t.Errorf("expected old_string_not_found, got %q", complete.Err)
	}
	// File must be unchanged
	got, _ := os.ReadFile(path)
	if string(got) != "hello world" {
		t.Errorf("file should be unchanged, got %q", got)
	}
}

func TestEditFileAmbiguousWithoutReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("foo\nfoo\n"), 0o644)
	allow := []string{dir + "/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "foo", NewString: "bar"},
		allow, 1<<20)
	if complete.Err != "ambiguous_match" {
		t.Errorf("expected ambiguous_match, got %q (%s)", complete.Err, complete.ErrDetail)
	}
	// Detail should tell AI how many matches there were
	if !strings.Contains(complete.ErrDetail, "2") {
		t.Errorf("ErrDetail should include match count: %q", complete.ErrDetail)
	}
	// File must be unchanged
	got, _ := os.ReadFile(path)
	if string(got) != "foo\nfoo\n" {
		t.Errorf("file should be unchanged on ambiguous_match")
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("foo\nfoo\nfoo\n"), 0o644)
	allow := []string{dir + "/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "foo", NewString: "bar", ReplaceAll: true},
		allow, 1<<20)
	if complete.Err != "" {
		t.Fatalf("unexpected err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "bar\nbar\nbar\n" {
		t.Errorf("replace_all output: %q", got)
	}
}

func TestEditFileEmptyOldString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("anything"), 0o644)
	allow := []string{dir + "/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "", NewString: "x"},
		allow, 1<<20)
	if complete.Err != "bad_payload" {
		t.Errorf("empty old_string should be bad_payload, got %q", complete.Err)
	}
}

func TestEditFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge")
	_ = os.WriteFile(path, make([]byte, 100), 0o644)
	allow := []string{dir + "/*"}
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "x", NewString: "y"},
		allow, 10) // cap=10, file=100
	if complete.Err != "exceeds_max_size" {
		t.Errorf("expected exceeds_max_size, got %q", complete.Err)
	}
}

func TestEditFileAtomicOnFailure(t *testing.T) {
	// After a successful edit, the file should reflect new content. We
	// can't easily inject a mid-write failure here, but we can at least
	// verify the file's i-node-stable behavior: original file is unchanged
	// when EditFile returns an error code that is supposed to be no-op.
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("orig"), 0o644)
	allow := []string{dir + "/*"}
	_, _ = EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "absent", NewString: "x"},
		allow, 1<<20)
	got, _ := os.ReadFile(path)
	if string(got) != "orig" {
		t.Errorf("file should be untouched on old_string_not_found, got %q", got)
	}
}
