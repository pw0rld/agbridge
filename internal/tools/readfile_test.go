package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/pw0rld/agbridge/internal/fileproto"
)

func TestReadFileHappy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	body := []byte("hello world\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	allow := []string{dir + "/*"}
	var got []byte
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, allow, 1<<20,
		func(c fileproto.FileChunk) { got = append(got, c.Data...) })
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "" {
		t.Errorf("complete.Err: %q", complete.Err)
	}
	if complete.Size != int64(len(body)) {
		t.Errorf("size: %d, want %d", complete.Size, len(body))
	}
	sum := sha256.Sum256(body)
	if complete.Sha256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 mismatch")
	}
	if string(got) != string(body) {
		t.Errorf("data: %q", got)
	}
}

func TestReadFileChunked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	body := make([]byte, 200*1024)
	for i := range body {
		body[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	allow := []string{dir + "/*"}
	chunks := 0
	var got []byte
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, allow, 1<<20,
		func(c fileproto.FileChunk) {
			chunks++
			got = append(got, c.Data...)
		})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if chunks < 3 {
		t.Errorf("expected >=3 chunks for 200KB, got %d", chunks)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}
	if complete.Size != int64(len(body)) {
		t.Errorf("size: %d", complete.Size)
	}
}

func TestReadFilePathForbidden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, []string{"/different/*"}, 1<<20,
		func(c fileproto.FileChunk) {})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "path_forbidden" {
		t.Errorf("err: %q", complete.Err)
	}
}

func TestReadFileNotFound(t *testing.T) {
	dir := t.TempDir()
	allow := []string{dir + "/*"}
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: filepath.Join(dir, "nope")}, allow, 1<<20,
		func(c fileproto.FileChunk) {})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err == "" {
		t.Error("expected err on missing file")
	}
}

func TestReadFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big")
	if err := os.WriteFile(path, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	allow := []string{dir + "/*"}
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, allow, 100,
		func(c fileproto.FileChunk) {})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "exceeds_max_size" {
		t.Errorf("err: %q", complete.Err)
	}
}
