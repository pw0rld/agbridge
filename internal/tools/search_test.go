package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/searchproto"
)

func setupSearchFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a.go":               "package a\n\nfunc Foo() {}\nfunc Bar() {}\n",
		"b.go":               "package b\n\nfunc Foo() {}\n",
		"sub/c.go":           "package c\n\nfunc Qux() {}\n",
		"sub/d.txt":          "hello world\nhello agbridge\n",
		"node_modules/x.go":  "package x\n\nfunc Foo() {}\n", // must be skipped
		".git/HEAD":          "ref: ...",                     // must be skipped
		"vendor/y.go":        "package y\n\nfunc Foo() {}\n", // must be skipped
		"binary.bin":         string([]byte{0, 1, 2, 3, 0, 4, 5, 'F', 'o', 'o', 0}),
	}
	for name, body := range files {
		full := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(body), 0o644)
	}
	return dir
}

func TestSearchHappyPath(t *testing.T) {
	dir := setupSearchFixture(t)
	allow := []string{dir + "/*", dir}
	resp := Search(searchproto.SearchRequest{Pattern: "Foo", Path: dir, Regex: true}, allow)
	if resp.Err != "" {
		t.Fatalf("err: %q (%s)", resp.Err, resp.ErrDetail)
	}
	// Should find Foo in a.go and b.go, NOT in node_modules/x.go or vendor/y.go
	if resp.TotalMatched < 2 || resp.TotalMatched > 4 {
		t.Fatalf("expected 2-3 matches, got %d", resp.TotalMatched)
	}
	for _, m := range resp.Results {
		if strings.Contains(m.File, "node_modules") || strings.Contains(m.File, "vendor") ||
			strings.Contains(m.File, ".git") {
			t.Errorf("should not match in ignored dir: %s", m.File)
		}
		if strings.Contains(m.File, "binary.bin") {
			t.Errorf("should not match in binary file: %s", m.File)
		}
	}
}

func TestSearchLineColumnSnippet(t *testing.T) {
	dir := setupSearchFixture(t)
	allow := []string{dir + "/*", dir}
	resp := Search(searchproto.SearchRequest{Pattern: "Bar", Path: dir, Regex: true}, allow)
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result for Bar, got %d", len(resp.Results))
	}
	m := resp.Results[0]
	if !strings.HasSuffix(m.File, "a.go") {
		t.Errorf("file: %s", m.File)
	}
	if m.Line != 4 {
		t.Errorf("line: %d (expected 4)", m.Line)
	}
	if m.Column != 6 {
		t.Errorf("column: %d (expected 6 for 'func Bar' = B at byte 6)", m.Column)
	}
	if m.Snippet != "func Bar() {}" {
		t.Errorf("snippet: %q", m.Snippet)
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	dir := setupSearchFixture(t)
	allow := []string{dir + "/*", dir}
	resp := Search(searchproto.SearchRequest{
		Pattern: "foo", Path: dir, Regex: true, CaseInsensitive: true}, allow)
	if resp.TotalMatched < 2 {
		t.Errorf("case insensitive should match Foo (capital): got %d", resp.TotalMatched)
	}
}

func TestSearchLiteralVsRegex(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f.txt"),
		[]byte("text with a.b.c and abc\n"), 0o644)
	allow := []string{dir + "/*", dir}

	// Literal mode: . is just a dot
	resp := Search(searchproto.SearchRequest{Pattern: "a.b.c", Path: dir, Regex: false}, allow)
	if resp.TotalMatched != 1 {
		t.Errorf("literal a.b.c should match once: %d", resp.TotalMatched)
	}

	// Regex mode: . matches any char so a.b.c also matches abc... but only on its own line
	// Actually a.b.c in regex means "a, any, b, any, c" so should also match "a.b.c"
	resp = Search(searchproto.SearchRequest{Pattern: "a.b.c", Path: dir, Regex: true}, allow)
	if resp.TotalMatched < 1 {
		t.Errorf("regex a.b.c should match at least once: %d", resp.TotalMatched)
	}
}

func TestSearchPathForbidden(t *testing.T) {
	dir := t.TempDir()
	allowlist := []string{"/different/x"}
	resp := Search(searchproto.SearchRequest{Pattern: "x", Path: dir, Regex: true}, allowlist)
	if resp.Err != "path_forbidden" {
		t.Errorf("err: %q", resp.Err)
	}
	if !strings.Contains(resp.ErrDetail, "/different/x") {
		t.Errorf("ErrDetail missing allowlist: %q", resp.ErrDetail)
	}
}

func TestSearchBadPattern(t *testing.T) {
	dir := t.TempDir()
	allow := []string{dir + "/*", dir}
	resp := Search(searchproto.SearchRequest{Pattern: "[unclosed", Path: dir, Regex: true}, allow)
	if resp.Err != "bad_pattern" {
		t.Errorf("err: %q", resp.Err)
	}
}

func TestSearchMaxResultsCap(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("hit\n", 50) // 50 hits
	_ = os.WriteFile(filepath.Join(dir, "many.txt"), []byte(body), 0o644)
	allow := []string{dir + "/*", dir}

	resp := Search(searchproto.SearchRequest{Pattern: "hit", Path: dir, Regex: true, MaxResults: 10}, allow)
	if len(resp.Results) != 10 {
		t.Errorf("expected 10 results, got %d", len(resp.Results))
	}
	if !resp.Truncated {
		t.Errorf("Truncated should be true when MaxResults hit")
	}
	if resp.TotalMatched != 50 {
		t.Errorf("TotalMatched should still count all: got %d", resp.TotalMatched)
	}
}

func TestSearchSkipsBinaryFile(t *testing.T) {
	dir := t.TempDir()
	// binary with NUL bytes
	_ = os.WriteFile(filepath.Join(dir, "bin"),
		[]byte{0, 1, 'm', 'a', 't', 'c', 'h', 0, 0}, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "txt"), []byte("match here\n"), 0o644)
	allow := []string{dir + "/*", dir}
	resp := Search(searchproto.SearchRequest{Pattern: "match", Path: dir, Regex: true}, allow)
	for _, m := range resp.Results {
		if strings.HasSuffix(m.File, "bin") {
			t.Errorf("binary file should be skipped, got match: %+v", m)
		}
	}
	if resp.TotalMatched != 1 {
		t.Errorf("only txt should match: got %d", resp.TotalMatched)
	}
}

func TestSearchGlobFilter(t *testing.T) {
	dir := setupSearchFixture(t)
	allow := []string{dir + "/*", dir}
	resp := Search(searchproto.SearchRequest{
		Pattern: "package", Path: dir, Regex: true, Glob: "*.go"}, allow)
	for _, m := range resp.Results {
		if !strings.HasSuffix(m.File, ".go") {
			t.Errorf("glob *.go should only match .go files, got %s", m.File)
		}
	}
}
