package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/fileproto"
)

func chunksFromBytes(data []byte, chunk int) func() (fileproto.FileChunk, error) {
	pos := 0
	return func() (fileproto.FileChunk, error) {
		if pos >= len(data) {
			return fileproto.FileChunk{Eof: true}, nil
		}
		end := pos + chunk
		if end > len(data) {
			end = len(data)
		}
		c := fileproto.FileChunk{
			Data: append([]byte(nil), data[pos:end]...),
			Eof:  end == len(data),
		}
		pos = end
		return c, nil
	}
}

func TestUploadChunksHappy(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	body := []byte("hello upload chunks\n")
	allow := []string{dir + "/*"}

	complete, err := UploadChunks(context.Background(),
		fileproto.UploadRequest{DestPath: dst, Mode: 0o644, Source: "chunks"},
		allow, chunksFromBytes(body, 5))
	if err != nil {
		t.Fatalf("UploadChunks: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("err: %q %s", complete.Err, complete.ErrDetail)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(body) {
		t.Errorf("content mismatch: %q", got)
	}
	if complete.Size != int64(len(body)) {
		t.Errorf("size: %d want %d", complete.Size, len(body))
	}
	want := sha256.Sum256(body)
	if complete.Sha256 != hex.EncodeToString(want[:]) {
		t.Errorf("sha256 mismatch")
	}
}

func TestUploadChunksPathForbidden(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allowlist := []string{"/different/a"}
	complete, _ := UploadChunks(context.Background(),
		fileproto.UploadRequest{DestPath: dst, Source: "chunks"},
		allowlist, chunksFromBytes([]byte("x"), 1))
	if complete.Err != "path_forbidden" {
		t.Errorf("err: %q", complete.Err)
	}
	if !strings.Contains(complete.ErrDetail, "/different/a") {
		t.Errorf("ErrDetail missing allowlist: %q", complete.ErrDetail)
	}
}

func TestUploadChunksShaMismatch(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allow := []string{dir + "/*"}
	complete, _ := UploadChunks(context.Background(),
		fileproto.UploadRequest{
			DestPath: dst, Source: "chunks",
			ExpectedSha256: "deadbeef" + strings.Repeat("0", 56),
		},
		allow, chunksFromBytes([]byte("hello"), 5))
	if complete.Err != "sha256_mismatch" {
		t.Errorf("err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	// File should NOT exist on sha mismatch (rollback)
	if _, err := os.Stat(dst); err == nil {
		t.Errorf("dest file should be absent on sha256_mismatch")
	}
}

func TestUploadChunksSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allow := []string{dir + "/*"}
	complete, _ := UploadChunks(context.Background(),
		fileproto.UploadRequest{DestPath: dst, Source: "chunks", ExpectedSize: 999},
		allow, chunksFromBytes([]byte("hi"), 2))
	if complete.Err != "size_mismatch" {
		t.Errorf("err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Errorf("dest file should be absent on size_mismatch")
	}
}

func TestUploadChunksAtomicWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allow := []string{dir + "/*"}
	// Pre-create the dest path with known content; if upload fails
	// mid-stream, original should remain untouched.
	_ = os.WriteFile(dst, []byte("ORIGINAL"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	complete, err := UploadChunks(ctx,
		fileproto.UploadRequest{DestPath: dst, Source: "chunks"},
		allow, chunksFromBytes([]byte("new"), 1))
	if err == nil && complete.Err == "" {
		t.Errorf("expected error on cancelled ctx")
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "ORIGINAL" {
		t.Errorf("original file disturbed: %q", got)
	}
}

func TestUploadURLHappy(t *testing.T) {
	body := []byte("hello from http server\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allow := []string{dir + "/*"}
	complete, err := UploadURL(context.Background(),
		fileproto.UploadRequest{DestPath: dst, Source: "url", URL: srv.URL, Mode: 0o600},
		allow)
	if err != nil {
		t.Fatalf("UploadURL: %v", err)
	}
	if complete.Err != "" {
		t.Fatalf("err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != string(body) {
		t.Errorf("content mismatch: %q want %q", got, body)
	}
	st, _ := os.Stat(dst)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode: %o", st.Mode().Perm())
	}
}

func TestUploadURL404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allow := []string{dir + "/*"}
	complete, _ := UploadURL(context.Background(),
		fileproto.UploadRequest{DestPath: dst, Source: "url", URL: srv.URL},
		allow)
	if complete.Err != "fetch_failed" {
		t.Errorf("err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	if !strings.Contains(complete.ErrDetail, "404") {
		t.Errorf("ErrDetail should include status: %q", complete.ErrDetail)
	}
}

func TestUploadURLShaMismatchAborts(t *testing.T) {
	body := []byte("hello\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	allow := []string{dir + "/*"}
	complete, _ := UploadURL(context.Background(),
		fileproto.UploadRequest{
			DestPath: dst, Source: "url", URL: srv.URL,
			ExpectedSha256: "0000000000000000000000000000000000000000000000000000000000000000",
		}, allow)
	if complete.Err != "sha256_mismatch" {
		t.Errorf("err: %q (%s)", complete.Err, complete.ErrDetail)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Errorf("dest must not exist on mismatch")
	}
}
