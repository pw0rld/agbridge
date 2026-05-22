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

// readChunkSize controls how many bytes each FileChunk carries. Bigger
// chunks = fewer WSS frames per file = less per-frame overhead. 1 MiB
// balances memory pressure against round-trip count for ~10 MiB files.
const readChunkSize = 1 * 1024 * 1024

// FileChunkEmitter is invoked for each chunk read from disk. The implementation
// is expected to wrap the chunk in a transport frame and send it.
type FileChunkEmitter func(c fileproto.FileChunk)

// ReadFile streams the file at req.Path back through onChunk. allowedPaths is
// the daemon-side allowlist; maxBytes caps the total bytes read (the caller
// usually passes the bridge output cap, e.g. 10 MB).
//
// Offset/Limit semantics:
//   - Offset > file size  -> 0 bytes read, no error (EOF immediately)
//   - Offset < 0          -> bad_payload
//   - Limit > 0           -> read at most Limit bytes after Offset
//   - Limit <= 0          -> read to EOF or maxBytes cap
//
// MaxSize on the request still caps total returned regardless of Limit.
//
// Returns FileComplete with Err set on any user-visible failure
// (path_forbidden, not_found, exceeds_max_size, bad_payload). Only
// context cancellation / IO errors are returned as the second value.
func ReadFile(ctx context.Context, req fileproto.FileReadRequest, allowedPaths []string, maxBytes int64, onChunk FileChunkEmitter) (fileproto.FileComplete, error) {
	if req.Offset < 0 {
		return fileproto.FileComplete{
			Err:       "bad_payload",
			ErrDetail: "offset must be >= 0",
		}, nil
	}
	if req.Limit < 0 {
		return fileproto.FileComplete{
			Err:       "bad_payload",
			ErrDetail: "limit must be >= 0 (0 means read to EOF)",
		}, nil
	}
	if _, ok := sandbox.PathAllowedResolved(req.Path, allowedPaths); !ok {
		return fileproto.FileComplete{
			Err:       "path_forbidden",
			ErrDetail: "path not in allowed_read_paths; allowed: " + sandbox.FormatAllowlist(allowedPaths),
		}, nil
	}
	f, err := os.Open(req.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileproto.FileComplete{Err: "not_found"}, nil
		}
		return fileproto.FileComplete{Err: "open_failed"}, nil
	}
	defer f.Close()

	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			return fileproto.FileComplete{Err: "read_failed"}, nil
		}
	}

	// hardCap (maxBytes ∩ MaxSize): file content exceeding this -> exceeds_max_size error.
	// softCap (Limit): polite truncate, no error.
	hardCap := maxBytes
	if req.MaxSize > 0 && int64(req.MaxSize) < hardCap {
		hardCap = int64(req.MaxSize)
	}
	softCap := int64(0)
	if req.Limit > 0 {
		softCap = req.Limit
	}

	hasher := sha256.New()
	var total int64
	buf := make([]byte, readChunkSize)
	for {
		if ctx.Err() != nil {
			return fileproto.FileComplete{}, ctx.Err()
		}
		// If softCap (Limit) reached, terminate cleanly.
		if softCap > 0 && total >= softCap {
			onChunk(fileproto.FileChunk{Eof: true})
			return fileproto.FileComplete{Size: total, Sha256: hex.EncodeToString(hasher.Sum(nil))}, nil
		}
		// Trim read to remaining softCap if it's tighter than buf.
		readN := int64(len(buf))
		if softCap > 0 {
			rem := softCap - total
			if rem < readN {
				readN = rem
			}
		}
		n, rerr := f.Read(buf[:readN])
		if n > 0 {
			if total+int64(n) > hardCap {
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
