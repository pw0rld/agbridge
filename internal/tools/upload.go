package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/sandbox"
)

// UploadMaxBytes is the size cap for upload destinations. Much higher
// than the 10 MiB read/write limit since upload is explicitly the path
// for "move a real file across" rather than "AI generates a config".
const UploadMaxBytes = 1 << 30 // 1 GiB

// UploadHTTPTimeout is the per-request timeout when source="url".
const UploadHTTPTimeout = 5 * time.Minute

// ChunkSource is the same shape used by WriteFile.
// (declared in writefile.go; reused here)

// UploadChunks writes a file via bridge-supplied FileChunk frames.
// Same atomic-temp+rename + sha256 semantics as WriteFile but caps at
// UploadMaxBytes and validates optional ExpectedSha256.
func UploadChunks(ctx context.Context, req fileproto.UploadRequest, allowedPaths []string, nextChunk ChunkSource) (fileproto.FileComplete, error) {
	if _, ok := sandbox.PathAllowedResolved(req.DestPath, allowedPaths); !ok {
		return fileproto.FileComplete{
			Err:       "path_forbidden",
			ErrDetail: "dest_path not in allowed_write_paths; allowed: " + sandbox.FormatAllowlist(allowedPaths),
		}, nil
	}
	tmp, tmpPath, err := openUploadTemp(req.DestPath, req.Mode)
	if err != nil {
		return fileproto.FileComplete{Err: "tmp_create_failed", ErrDetail: err.Error()}, nil
	}

	hasher := sha256.New()
	var total int64
	for {
		if ctx.Err() != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fileproto.FileComplete{}, ctx.Err()
		}
		c, srcErr := nextChunk()
		if srcErr != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return fileproto.FileComplete{Size: total, Err: fmt.Sprintf("source_error: %v", srcErr)}, nil
		}
		if len(c.Data) > 0 {
			if total+int64(len(c.Data)) > UploadMaxBytes {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				return fileproto.FileComplete{Size: total, Err: "exceeds_max_size",
					ErrDetail: fmt.Sprintf("upload cap is %d bytes", UploadMaxBytes)}, nil
			}
			if _, err := tmp.Write(c.Data); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				return fileproto.FileComplete{Size: total, Err: "write_failed", ErrDetail: err.Error()}, nil
			}
			hasher.Write(c.Data)
			total += int64(len(c.Data))
		}
		if c.Eof {
			break
		}
	}

	return finalizeUpload(tmp, tmpPath, req.DestPath, req.Mode, total, hasher, req.ExpectedSize, req.ExpectedSha256)
}

// UploadURL has the daemon fetch the URL and stream the body to the
// destination. Bridge does not transfer the bytes. Supports HTTP/HTTPS.
func UploadURL(ctx context.Context, req fileproto.UploadRequest, allowedPaths []string) (fileproto.FileComplete, error) {
	if _, ok := sandbox.PathAllowedResolved(req.DestPath, allowedPaths); !ok {
		return fileproto.FileComplete{
			Err:       "path_forbidden",
			ErrDetail: "dest_path not in allowed_write_paths; allowed: " + sandbox.FormatAllowlist(allowedPaths),
		}, nil
	}
	if req.URL == "" {
		return fileproto.FileComplete{Err: "bad_payload", ErrDetail: "url is required when source=url"}, nil
	}

	httpCtx, cancel := context.WithTimeout(ctx, UploadHTTPTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(httpCtx, http.MethodGet, req.URL, nil)
	if err != nil {
		return fileproto.FileComplete{Err: "bad_payload", ErrDetail: "invalid url: " + err.Error()}, nil
	}
	httpReq.Header.Set("User-Agent", "agbridge-upload/0.2")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fileproto.FileComplete{Err: "fetch_failed", ErrDetail: err.Error()}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fileproto.FileComplete{Err: "fetch_failed",
			ErrDetail: fmt.Sprintf("HTTP %d from upstream", resp.StatusCode)}, nil
	}

	tmp, tmpPath, err := openUploadTemp(req.DestPath, req.Mode)
	if err != nil {
		return fileproto.FileComplete{Err: "tmp_create_failed", ErrDetail: err.Error()}, nil
	}

	hasher := sha256.New()
	limit := int64(UploadMaxBytes) + 1
	limited := io.LimitReader(resp.Body, limit)
	mw := io.MultiWriter(tmp, hasher)
	n, err := io.Copy(mw, limited)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: n, Err: "fetch_failed", ErrDetail: err.Error()}, nil
	}
	if n > UploadMaxBytes {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: n, Err: "exceeds_max_size",
			ErrDetail: fmt.Sprintf("URL response > %d byte upload cap", UploadMaxBytes)}, nil
	}

	return finalizeUpload(tmp, tmpPath, req.DestPath, req.Mode, n, hasher, req.ExpectedSize, req.ExpectedSha256)
}

func openUploadTemp(destPath string, mode uint32) (*os.File, string, error) {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	tmp, err := os.CreateTemp(dir, ".agbridge-upload-*")
	if err != nil {
		return nil, "", err
	}
	if mode != 0 {
		_ = os.Chmod(tmp.Name(), os.FileMode(mode))
	}
	return tmp, tmp.Name(), nil
}

func finalizeUpload(tmp *os.File, tmpPath, destPath string, mode uint32, total int64, hasher hashWriter, expSize int64, expSha string) (fileproto.FileComplete, error) {
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: total, Err: "write_failed", ErrDetail: err.Error()}, nil
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: total, Err: "close_failed", ErrDetail: err.Error()}, nil
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if expSha != "" && sum != expSha {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{
			Size: total, Err: "sha256_mismatch",
			ErrDetail: fmt.Sprintf("expected %s got %s", expSha, sum),
		}, nil
	}
	if expSize > 0 && expSize != total {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{
			Size: total, Err: "size_mismatch",
			ErrDetail: fmt.Sprintf("expected %d got %d", expSize, total),
		}, nil
	}
	if mode != 0 {
		_ = os.Chmod(tmpPath, os.FileMode(mode))
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: total, Err: "rename_failed", ErrDetail: err.Error()}, nil
	}
	return fileproto.FileComplete{Size: total, Sha256: sum}, nil
}

// hashWriter narrows hash.Hash to what finalizeUpload needs (lets us
// avoid importing crypto/hash in the signature).
type hashWriter interface {
	io.Writer
	Sum(b []byte) []byte
}
