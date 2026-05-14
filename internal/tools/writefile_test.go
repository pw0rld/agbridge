package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pw0rld/agbridge/internal/fileproto"
)

func chunksFrom(data []byte, chunkSize int) func() (fileproto.FileChunk, error) {
	pos := 0
	return func() (fileproto.FileChunk, error) {
		if pos >= len(data) {
			return fileproto.FileChunk{Eof: true}, nil
		}
		end := pos + chunkSize
		if end > len(data) {
			end = len(data)
		}
		c := fileproto.FileChunk{Data: append([]byte(nil), data[pos:end]...)}
		pos = end
		if pos >= len(data) {
			c.Eof = true
		}
		return c, nil
	}
}

func TestWriteFileHappy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	allow := []string{dir + "/*"}
	body := []byte("hello write\n")
	complete, err := WriteFile(context.Background(),
		fileproto.FileWriteRequest{Path: path, Mode: 0o600}, allow, 1<<20,
		chunksFrom(body, 1024))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("complete.Err: %q", complete.Err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("file: %q, want %q", got, body)
	}
	if complete.Size != int64(len(body)) {
		t.Errorf("size: %d", complete.Size)
	}
	sum := sha256.Sum256(body)
	if complete.Sha256 != hex.EncodeToString(sum[:]) {
		t.Errorf("sha256 mismatch")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v", info.Mode())
	}
}

func TestWriteFileChunked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	allow := []string{dir + "/*"}
	body := make([]byte, 200*1024)
	for i := range body {
		body[i] = byte(i)
	}
	_, err := WriteFile(context.Background(),
		fileproto.FileWriteRequest{Path: path}, allow, 1<<20,
		chunksFrom(body, 32*1024))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}
}

func TestWriteFilePathForbidden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	complete, err := WriteFile(context.Background(),
		fileproto.FileWriteRequest{Path: path}, []string{"/different/*"}, 1<<20,
		chunksFrom([]byte("x"), 1))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if complete.Err != "path_forbidden" {
		t.Errorf("err: %q", complete.Err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file should not exist")
	}
}

func TestWriteFileExceedsMaxSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge")
	allow := []string{dir + "/*"}
	complete, err := WriteFile(context.Background(),
		fileproto.FileWriteRequest{Path: path}, allow, 100,
		chunksFrom(make([]byte, 1024), 64))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if complete.Err != "exceeds_max_size" {
		t.Errorf("err: %q", complete.Err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("target file should not exist")
	}
}

func TestWriteFileAtomicOnSourceError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "victim")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	allow := []string{dir + "/*"}
	failing := func() (fileproto.FileChunk, error) {
		return fileproto.FileChunk{}, io.ErrUnexpectedEOF
	}
	complete, err := WriteFile(context.Background(),
		fileproto.FileWriteRequest{Path: path}, allow, 1<<20, failing)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if complete.Err == "" {
		t.Error("expected complete.Err on chunk source failure")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Errorf("target was overwritten: %q", got)
	}
}
