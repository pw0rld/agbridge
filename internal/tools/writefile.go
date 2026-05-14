package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/sandbox"
)

// ChunkSource yields successive FileChunk frames. WriteFile loops until it
// receives a chunk with Eof=true (or an error).
type ChunkSource func() (fileproto.FileChunk, error)

// WriteFile receives chunks via nextChunk and writes them to a temp file in
// the same directory as req.Path, then renames into place. allowedPaths is the
// daemon-side allowlist; maxBytes caps total bytes received.
//
// Returns FileComplete with Err set on user-visible failures
// (path_forbidden, exceeds_max_size, write_failed, source_error). The target
// file is never partially overwritten — failures leave the original (if any)
// in place.
func WriteFile(ctx context.Context, req fileproto.FileWriteRequest, allowedPaths []string, maxBytes int64, nextChunk ChunkSource) (fileproto.FileComplete, error) {
	if !sandbox.PathAllowed(req.Path, allowedPaths) {
		return fileproto.FileComplete{Err: "path_forbidden"}, nil
	}
	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}

	dir := filepath.Dir(req.Path)
	tmp, err := os.CreateTemp(dir, ".agbridge-write-*")
	if err != nil {
		return fileproto.FileComplete{Err: "tmp_create_failed"}, nil
	}
	tmpPath := tmp.Name()
	cleanupTmp := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	hasher := sha256.New()
	var total int64
	for {
		if ctx.Err() != nil {
			cleanupTmp()
			return fileproto.FileComplete{}, ctx.Err()
		}
		c, err := nextChunk()
		if err != nil {
			cleanupTmp()
			return fileproto.FileComplete{Size: total, Err: fmt.Sprintf("source_error: %v", err)}, nil
		}
		if len(c.Data) > 0 {
			if total+int64(len(c.Data)) > maxBytes {
				cleanupTmp()
				return fileproto.FileComplete{Size: total, Err: "exceeds_max_size"}, nil
			}
			if _, werr := tmp.Write(c.Data); werr != nil {
				cleanupTmp()
				return fileproto.FileComplete{Size: total, Err: "write_failed"}, nil
			}
			hasher.Write(c.Data)
			total += int64(len(c.Data))
		}
		if c.Eof {
			break
		}
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: total, Err: "close_failed"}, nil
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: total, Err: "chmod_failed"}, nil
	}
	if err := os.Rename(tmpPath, req.Path); err != nil {
		_ = os.Remove(tmpPath)
		return fileproto.FileComplete{Size: total, Err: "rename_failed"}, nil
	}
	return fileproto.FileComplete{Size: total, Sha256: hex.EncodeToString(hasher.Sum(nil))}, nil
}
