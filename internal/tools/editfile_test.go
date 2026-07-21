package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/fileproto"
)

func TestEditFileHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	_ = os.WriteFile(path, []byte("package x\n\nfunc Old() {}\n"), 0o644)

	complete, err := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "func Old()", NewString: "func New()"},
		1<<20)
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
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: filepath.Join(dir, "nope.txt"), OldString: "a", NewString: "b"},
		1<<20)
	if complete.Err != "not_found" {
		t.Errorf("expected not_found, got %q", complete.Err)
	}
}

func TestEditFileNoPathGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "x", NewString: "y"},
		1<<20)
	if complete.Err != "" {
		t.Errorf("unexpected err: %q (%s)", complete.Err, complete.ErrDetail)
	}
}

func TestEditFileOldStringNotPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("hello world"), 0o644)
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "absent", NewString: "x"},
		1<<20)
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
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "foo", NewString: "bar"},
		1<<20)
	if complete.Err != "ambiguous_match" {
		t.Errorf("expected ambiguous_match, got %q (%s)", complete.Err, complete.ErrDetail)
	}
	// Detail should tell AI how many matches there were AND which lines
	if !strings.Contains(complete.ErrDetail, "2") {
		t.Errorf("ErrDetail should include match count: %q", complete.ErrDetail)
	}
	if !strings.Contains(complete.ErrDetail, "lines") || !strings.Contains(complete.ErrDetail, "1, 2") {
		t.Errorf("ErrDetail should include line numbers, got: %q", complete.ErrDetail)
	}
	// File must be unchanged
	got, _ := os.ReadFile(path)
	if string(got) != "foo\nfoo\n" {
		t.Errorf("file should be unchanged on ambiguous_match")
	}
}

func TestEditFileMatchLinesAcrossFile(t *testing.T) {
	body := "alpha\nbeta\nfoo\ngamma\nfoo\ndelta\nfoo here\n"
	got := matchLines(body, "foo")
	want := []int{3, 5, 7}
	if len(got) != len(want) {
		t.Fatalf("got lines %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("at idx %d got %d want %d", i, got[i], want[i])
		}
	}
}

func TestEditFileWritesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orig.txt")
	orig := []byte("original content here\n")
	_ = os.WriteFile(path, orig, 0o644)

	complete, err := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "original", NewString: "MODIFIED"},
		1<<20)
	if err != nil {
		t.Fatalf("EditFile: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	if complete.BackupPath == "" {
		t.Fatal("BackupPath should be set after successful edit")
	}
	if !strings.HasPrefix(complete.BackupPath, path+".bak.") {
		t.Errorf("BackupPath should start with %s.bak.: got %s", path, complete.BackupPath)
	}
	backupBytes, err := os.ReadFile(complete.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupBytes) != string(orig) {
		t.Errorf("backup content mismatch: got %q want %q", backupBytes, orig)
	}
	// And the actual file got edited
	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(cur), "MODIFIED") {
		t.Errorf("primary file not edited: %s", cur)
	}
}

func TestEditFileBackupGCKeepsOnlyN(t *testing.T) {
	// Successive edits should retain at most BackupRetainCount=5 .bak files.
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.txt")
	_ = os.WriteFile(path, []byte("v0"), 0o644)

	for i := 0; i < 10; i++ {
		old := fmt.Sprintf("v%d", i)
		nw := fmt.Sprintf("v%d", i+1)
		complete, err := EditFile(context.Background(),
			fileproto.FileEditRequest{Path: path, OldString: old, NewString: nw},
			1<<20)
		if err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		if complete.Err != "" {
			t.Fatalf("edit %d err: %q", i, complete.Err)
		}
		// Sleep a hair so mtimes are distinct (POSIX nanosecond res not
		// always reliable, especially on tmpfs).
		time.Sleep(2 * time.Millisecond)
	}
	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != BackupRetainCount {
		t.Fatalf("expected exactly %d backups after GC, got %d: %v",
			BackupRetainCount, len(matches), matches)
	}
	// File should be at final v10
	final, _ := os.ReadFile(path)
	if string(final) != "v10" {
		t.Errorf("file should be v10, got %q", final)
	}
}

func TestEditFileBackupGCKeepsNewest(t *testing.T) {
	// After GC, the surviving backups should be the most recent ones —
	// older ones get deleted.
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("step-0"), 0o644)

	for i := 0; i < 8; i++ {
		old := fmt.Sprintf("step-%d", i)
		nw := fmt.Sprintf("step-%d", i+1)
		_, err := EditFile(context.Background(),
			fileproto.FileEditRequest{Path: path, OldString: old, NewString: nw},
			1<<20)
		if err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != BackupRetainCount {
		t.Fatalf("expected %d, got %d", BackupRetainCount, len(matches))
	}
	// Among surviving backups, the contents should cover the LATEST
	// pre-edit states (step-3 through step-7), not the earliest.
	versions := map[string]bool{}
	for _, m := range matches {
		body, _ := os.ReadFile(m)
		versions[string(body)] = true
	}
	// The first 3 pre-edit states (step-0, step-1, step-2) should have been GC'd.
	for _, gone := range []string{"step-0", "step-1", "step-2"} {
		if versions[gone] {
			t.Errorf("expected %q to be GC'd, but it survived", gone)
		}
	}
	// The last few (step-5..step-7) must survive.
	for _, kept := range []string{"step-5", "step-6", "step-7"} {
		if !versions[kept] {
			t.Errorf("expected %q to survive GC, but it was deleted", kept)
		}
	}
}

func TestEditFileBackupNotCreatedOnPreCheckErrors(t *testing.T) {
	// If the edit fails its pre-checks (not_found /
	// old_string_not_found / ambiguous_match / etc.), no backup should
	// be written and BackupPath should be empty.
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("hello"), 0o644)
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "absent", NewString: "x"},
		1<<20)
	if complete.Err != "old_string_not_found" {
		t.Fatalf("expected old_string_not_found, got %q", complete.Err)
	}
	if complete.BackupPath != "" {
		t.Errorf("BackupPath should be empty when edit fails: got %q", complete.BackupPath)
	}
	matches, _ := filepath.Glob(path + ".bak.*")
	if len(matches) != 0 {
		t.Errorf("no .bak file should exist after failed edit, got: %v", matches)
	}
}

func TestEditFileReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("foo\nfoo\nfoo\n"), 0o644)
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "foo", NewString: "bar", ReplaceAll: true},
		1<<20)
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
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "", NewString: "x"},
		1<<20)
	if complete.Err != "bad_payload" {
		t.Errorf("empty old_string should be bad_payload, got %q", complete.Err)
	}
}

func TestEditFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge")
	_ = os.WriteFile(path, make([]byte, 100), 0o644)
	complete, _ := EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "x", NewString: "y"},
		10) // cap=10, file=100
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
	_, _ = EditFile(context.Background(),
		fileproto.FileEditRequest{Path: path, OldString: "absent", NewString: "x"},
		1<<20)
	got, _ := os.ReadFile(path)
	if string(got) != "orig" {
		t.Errorf("file should be untouched on old_string_not_found, got %q", got)
	}
}
