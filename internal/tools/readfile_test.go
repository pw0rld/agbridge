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
	var got []byte
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, 1<<20,
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
	// Use a file slightly larger than readChunkSize (1 MiB) so we exercise
	// the streaming loop without being absurdly big in test.
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	body := make([]byte, readChunkSize+200*1024) // 1.2 MiB
	for i := range body {
		body[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	chunks := 0
	var got []byte
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, int64(len(body)+1),
		func(c fileproto.FileChunk) {
			chunks++
			got = append(got, c.Data...)
		})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if chunks < 2 {
		t.Errorf("expected >=2 chunks for file larger than readChunkSize, got %d", chunks)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}
	if complete.Size != int64(len(body)) {
		t.Errorf("size: %d", complete.Size)
	}
}

func TestReadFileNoPathGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, 1<<20,
		func(c fileproto.FileChunk) {})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "" {
		t.Errorf("unexpected err: %q / %q", complete.Err, complete.ErrDetail)
	}
}

func TestReadFileNotFound(t *testing.T) {
	dir := t.TempDir()
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: filepath.Join(dir, "nope")}, 1<<20,
		func(c fileproto.FileChunk) {})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err == "" {
		t.Error("expected err on missing file")
	}
}

func TestReadFileWithOffsetAndLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	data := []byte("0123456789ABCDEFGHIJ") // 20 bytes
	_ = os.WriteFile(path, data, 0o644)

	var got []byte
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path, Offset: 10, Limit: 5},
		1<<20,
		func(c fileproto.FileChunk) { got = append(got, c.Data...) })
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("err: %q", complete.Err)
	}
	if string(got) != "ABCDE" {
		t.Errorf("expected ABCDE, got %q", got)
	}
	if complete.Size != 5 {
		t.Errorf("size: %d", complete.Size)
	}
	// sha256 should be of the 5 returned bytes, not the whole file
	expected := sha256.Sum256([]byte("ABCDE"))
	if complete.Sha256 != hex.EncodeToString(expected[:]) {
		t.Errorf("sha256 of returned data wrong: %s", complete.Sha256)
	}
}

func TestReadFileOffsetPastEOF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("short"), 0o644)
	var emitted bool
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path, Offset: 1000},
		1<<20,
		func(c fileproto.FileChunk) {
			if !c.Eof || len(c.Data) > 0 {
				emitted = true
			}
		})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "" {
		t.Errorf("err: %q", complete.Err)
	}
	if complete.Size != 0 {
		t.Errorf("size: %d", complete.Size)
	}
	if emitted {
		t.Errorf("should not have emitted any data past EOF")
	}
}

func TestReadFileNegativeOffsetRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	complete, _ := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path, Offset: -1},
		1<<20,
		func(c fileproto.FileChunk) {})
	if complete.Err != "bad_payload" {
		t.Errorf("expected bad_payload, got %q", complete.Err)
	}
}

func TestReadFileLimitLargerThanFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("abc"), 0o644)
	var got []byte
	complete, _ := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path, Limit: 999},
		1<<20,
		func(c fileproto.FileChunk) { got = append(got, c.Data...) })
	if string(got) != "abc" || complete.Size != 3 {
		t.Errorf("Limit > file should read full file, got %q size=%d", got, complete.Size)
	}
}

func TestReadFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big")
	if err := os.WriteFile(path, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	complete, err := ReadFile(context.Background(),
		fileproto.FileReadRequest{Path: path}, 100,
		func(c fileproto.FileChunk) {})
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if complete.Err != "exceeds_max_size" {
		t.Errorf("err: %q", complete.Err)
	}
}
