package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/sandbox"
)

const readChunkSize = 64 * 1024

// FileChunkEmitter is invoked for each chunk read from disk. The implementation
// is expected to wrap the chunk in a transport frame and send it.
type FileChunkEmitter func(c fileproto.FileChunk)

// ReadFile streams the file at req.Path back through onChunk. allowedPaths is
// the daemon-side allowlist; maxBytes caps the total bytes read (the caller
// usually passes the bridge output cap, e.g. 10 MB).
//
// Returns FileComplete with Err set on any user-visible failure
// (path_forbidden, not_found, exceeds_max_size). Only context cancellation /
// IO errors are returned as the second value.
func ReadFile(ctx context.Context, req fileproto.FileReadRequest, allowedPaths []string, maxBytes int64, onChunk FileChunkEmitter) (fileproto.FileComplete, error) {
	if !sandbox.PathAllowed(req.Path, allowedPaths) {
		return fileproto.FileComplete{Err: "path_forbidden"}, nil
	}
	f, err := os.Open(req.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileproto.FileComplete{Err: "not_found"}, nil
		}
		return fileproto.FileComplete{Err: "open_failed"}, nil
	}
	defer f.Close()

	cap := maxBytes
	if req.MaxSize > 0 && int64(req.MaxSize) < cap {
		cap = int64(req.MaxSize)
	}

	hasher := sha256.New()
	var total int64
	buf := make([]byte, readChunkSize)
	for {
		if ctx.Err() != nil {
			return fileproto.FileComplete{}, ctx.Err()
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			if total+int64(n) > cap {
				return fileproto.FileComplete{Size: total, Err: "exceeds_max_size"}, nil
			}
			total += int64(n)
			hasher.Write(buf[:n])
			data := make([]byte, n)
			copy(data, buf[:n])
			eof := rerr == io.EOF
			onChunk(fileproto.FileChunk{Data: data, Eof: eof})
			if eof {
				return fileproto.FileComplete{Size: total, Sha256: hex.EncodeToString(hasher.Sum(nil))}, nil
			}
		}
		if rerr == io.EOF {
			onChunk(fileproto.FileChunk{Eof: true})
			return fileproto.FileComplete{Size: total, Sha256: hex.EncodeToString(hasher.Sum(nil))}, nil
		}
		if rerr != nil {
			return fileproto.FileComplete{Size: total, Err: "read_failed"}, nil
		}
	}
}
