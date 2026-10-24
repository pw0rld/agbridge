package cliclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/proto"
)

// Whole-file downloads to dest_path of at least this size are fetched as
// concurrent offset-range segments instead of one sequential stream.
// var (not const) so tests can lower it; do not mutate at runtime in
// production (same convention as maxBufferedOutput).
var parallelDownloadThreshold int64 = 16 << 20

// Segment sizing: aim for ~8 MiB per segment, clamped to [2, 4]
// segments. More parallelism would help marginally but multiplies the
// daemon-side open files and gateway frame interleaving for little
// gain on typical WAN links.
const (
	parallelSegmentTarget = int64(8 << 20)
	parallelSegmentsMin   = 2
	parallelSegmentsMax   = 4
)

// finalizeTempDownload chmods and closes the streamed temp file, then
// renames it onto dest. Temp and dest share a directory, so rename is
// atomic on POSIX: readers see either the old file or the complete new
// one, never a truncated partial. On any error the file is closed and the
// caller must remove the temp path.
func finalizeTempDownload(tmp *os.File, dest string) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(dest); err == nil {
		mode = fi.Mode().Perm() // replacing an existing file: keep its perms
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

type uploadArgs struct {
	DestPath       string   `json:"dest_path"`
	Source         string   `json:"source"`
	ChunksB64      []string `json:"chunks_b64,omitempty"`
	URL            string   `json:"url,omitempty"`
	LocalPath      string   `json:"local_path,omitempty"`
	Mode           uint32   `json:"mode,omitempty"`
	ExpectedSize   int64    `json:"expected_size,omitempty"`
	ExpectedSha256 string   `json:"expected_sha256,omitempty"`
}

// UploadHandler transfers a file to the daemon. Source modes:
//   - "chunks": inline base64 chunks (for small embedded content)
//   - "url": daemon fetches from URL
//   - "local_path": bridge reads from its own disk and streams chunks
//
// Daemon writes atomically via temp + rename. 1 GiB total cap.
func (r *Router) UploadHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args uploadArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.DestPath == "" {
		return mcpErrorResult(errcode.New("bad_payload", "dest_path required")), nil
	}
	switch args.Source {
	case "chunks":
		if len(args.ChunksB64) == 0 {
			return mcpErrorResult(errcode.New("bad_payload", "chunks_b64 must be non-empty for source=chunks")), nil
		}
	case "url":
		if args.URL == "" {
			return mcpErrorResult(errcode.New("bad_payload", "url required for source=url")), nil
		}
	case "local_path":
		if args.LocalPath == "" {
			return mcpErrorResult(errcode.New("bad_payload", "local_path required for source=local_path")), nil
		}
		if _, err := os.Stat(args.LocalPath); err != nil {
			return mcpErrorResult(errcode.New("bad_payload", "local_path: "+err.Error())), nil
		}
	default:
		return mcpErrorResult(errcode.New("bad_payload", "source must be 'chunks', 'url', or 'local_path'")), nil
	}

	reqID := newReqID()
	// For local_path, the bridge reads the file locally and streams chunks to
	// the daemon — from the daemon's perspective this is a "chunks" upload.
	daemonSource := args.Source
	if daemonSource == "local_path" {
		daemonSource = "chunks"
	}
	reqJSON, _ := fileproto.UploadRequest{
		DestPath:       args.DestPath,
		Mode:           args.Mode,
		Source:         daemonSource,
		URL:            args.URL,
		ExpectedSize:   args.ExpectedSize,
		ExpectedSha256: args.ExpectedSha256,
	}.Encode()

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	// We send the chunk frames, so gate binary on the daemon's
	// advertisement (an old daemon silently drops unknown frame types).
	// The probe result is cached for the connection's lifetime.
	binary := false
	if daemonSource == "chunks" {
		binary = r.supportsBinaryChunks(ctx)
	}

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeUploadRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	// For chunks source, stream the base64 chunks now. Last one with Eof=true.
	if args.Source == "chunks" {
		for i, c := range args.ChunksB64 {
			data, err := base64.StdEncoding.DecodeString(c)
			if err != nil {
				return mcpErrorResult(errcode.New("bad_payload", fmt.Sprintf("chunks_b64[%d]: %v", i, err))), nil
			}
			eof := i == len(args.ChunksB64)-1
			if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Data: data, Eof: eof}, binary); err != nil {
				return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
			}
		}
	}

	// For local_path source, open the file and stream 1 MiB chunks.
	if args.Source == "local_path" {
		f, err := os.Open(args.LocalPath)
		if err != nil {
			return mcpErrorResult(errcode.New("bad_payload", "open local_path: "+err.Error())), nil
		}
		defer f.Close()
		const chunkSize = 1 << 20
		buf := make([]byte, chunkSize)
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				eof := rerr == io.EOF
				if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Data: data, Eof: eof}, binary); err != nil {
					return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
				}
				if eof {
					break
				}
			}
			if rerr == io.EOF {
				// Zero-length file: send a single empty Eof chunk so daemon closes the write cleanly.
				if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Eof: true}, binary); err != nil {
					return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
				}
				break
			}
			if rerr != nil {
				return mcpErrorResult(errcode.New("bad_payload", "local_path read: "+rerr.Error())), nil
			}
		}
	}

	select {
	case <-ctx.Done():
		return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
	case f, ok := <-ch:
		if !ok {
			return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
		}
		switch f.Type {
		case proto.FrameTypeFileComplete:
			c, err := fileproto.DecodeFileComplete(f.Payload)
			if err != nil {
				return mcpErrorResult(errcode.New("bad_payload", "upload: "+err.Error())), nil
			}
			if c.Err != "" {
				msg := "upload: " + c.Err
				if c.ErrDetail != "" {
					msg = msg + " (" + c.ErrDetail + ")"
				}
				return mcpErrorResult(errcode.New(c.Err, msg)), nil
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text",
					"text": fmt.Sprintf("uploaded %d bytes to %s (sha256=%s)", c.Size, args.DestPath, c.Sha256)}},
				"_meta": map[string]any{
					"size":      c.Size,
					"sha256":    c.Sha256,
					"dest_path": args.DestPath,
				},
			}, nil
		case proto.FrameTypeError:
			code, detail := errcode.DecodeFramePayload(f.Payload)
			msg := "upload: " + code
			if detail != "" {
				msg = msg + " (" + detail + ")"
			}
			return mcpErrorResult(errcode.New(code, msg)), nil
		}
		return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame %d", f.Type))), nil
	}
}

type downloadArgs struct {
	SrcPath  string `json:"src_path"`
	DestPath string `json:"dest_path,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Limit    int64  `json:"limit,omitempty"`
}

// DownloadHandler streams a file from the daemon. With dest_path the
// content is written straight to local disk and only metadata is
// returned; without it, content is inlined (capped at 10 MB).
func (r *Router) DownloadHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args downloadArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.SrcPath == "" {
		return mcpErrorResult(errcode.New("bad_payload", "src_path required")), nil
	}

	// Whole-file fetch to local disk: try a segmented parallel fetch for
	// large files. Offset/Limit are already in the protocol and the
	// daemon serves each download request on its own goroutine, so
	// segments are just concurrent downloads — no capability probe
	// needed (every daemon since phase 4 honors Offset/Limit, and both
	// chunk encodings are accepted on receive). Any miss — stat
	// failure, small file, or a failed segment — falls through to the
	// sequential path below, which then reports the authoritative
	// error to the user. The extra stat costs one small RTT on the
	// dest_path path only; inline (no dest_path) reads are small by
	// construction and never stat.
	if args.DestPath != "" && args.Offset == 0 && args.Limit == 0 {
		if size, ok := r.statRemoteSize(ctx, args.SrcPath); ok && size >= parallelDownloadThreshold {
			if res, perr := r.downloadParallel(ctx, args, size); perr == nil {
				return res, nil
			}
		}
	}

	reqID := newReqID()
	// Binary=true is always requested: an old daemon ignores the unknown
	// field and replies with JSON chunks, which the loop below accepts
	// too — no capabilities round-trip needed on receive paths.
	reqJSON, _ := fileproto.DownloadRequest{
		SrcPath: args.SrcPath, Offset: args.Offset, Limit: args.Limit, Binary: true,
	}.Encode()

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)
	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeDownloadRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	emit := EmitterFromContext(ctx)
	var bytesReceived int64

	// If dest_path supplied, stream to local file (no buffering all in
	// memory). Otherwise buffer and return inline.
	//
	// Never open DestPath directly: a failed or cancelled download must
	// not truncate or corrupt a pre-existing file. Stream to a temp file
	// in the same directory (rename needs the same filesystem) and rename
	// into place only after FileComplete arrives without an error.
	var (
		tmp     *os.File // nil after successful rename; nil-check in cleanup tells the two apart
		content []byte
	)
	if args.DestPath != "" {
		t, err := os.CreateTemp(filepath.Dir(args.DestPath), "."+filepath.Base(args.DestPath)+".agbridge-*.tmp")
		if err != nil {
			return mcpErrorResult(errcode.New("write_failed", err.Error())), nil
		}
		tmp = t
		defer func() {
			if tmp != nil { // download never finalized → discard temp
				_ = tmp.Close()
				_ = os.Remove(t.Name())
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case fr, ok := <-ch:
			if !ok {
				return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
			}
			switch fr.Type {
			case proto.FrameTypeFileChunk, proto.FrameTypeFileChunkBin:
				c, err := decodeChunk(fr.Type, fr.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				if tmp != nil {
					if _, err := tmp.Write(c.Data); err != nil {
						return mcpErrorResult(errcode.New("write_failed", err.Error())), nil
					}
				} else {
					if int64(len(content))+int64(len(c.Data)) > maxBufferedOutput {
						return mcpErrorResult(errcode.New("exceeds_max_size",
							"download without dest_path exceeds 10 MB cap; pass dest_path to stream to disk")), nil
					}
					content = append(content, c.Data...)
				}
				// Skip emit for EOF sentinels (Data==nil, Eof==true); only data-bearing chunks advance progress.
				if len(c.Data) > 0 {
					bytesReceived += int64(len(c.Data))
					emit(float64(bytesReceived), nil, "")
				}
			case proto.FrameTypeFileComplete:
				c, err := fileproto.DecodeFileComplete(fr.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", "download: "+err.Error())), nil
				}
				if c.Err != "" {
					msg := "download: " + c.Err
					if c.ErrDetail != "" {
						msg = msg + " (" + c.ErrDetail + ")"
					}
					return mcpErrorResult(errcode.New(c.Err, msg)), nil
				}
				if tmp != nil {
					if err := finalizeTempDownload(tmp, args.DestPath); err != nil {
						name := tmp.Name()
						tmp = nil // already closed by finalizeTempDownload
						_ = os.Remove(name)
						return mcpErrorResult(errcode.New("write_failed", err.Error())), nil
					}
					tmp = nil // renamed into place; defer must not remove it
				}
				meta := map[string]any{
					"size":     c.Size,
					"sha256":   c.Sha256,
					"src_path": args.SrcPath,
				}
				var text string
				if args.DestPath != "" {
					meta["dest_path"] = args.DestPath
					text = fmt.Sprintf("downloaded %d bytes to %s (sha256=%s)", c.Size, args.DestPath, c.Sha256)
				} else if utf8.Valid(content) {
					text = string(content)
				} else {
					meta["content_b64"] = base64.StdEncoding.EncodeToString(content)
					text = fmt.Sprintf("(binary %d bytes, sha256=%s)", c.Size, c.Sha256)
				}
				return map[string]any{
					"content": []any{map[string]any{"type": "text", "text": text}},
					"_meta":   meta,
				}, nil
			case proto.FrameTypeError:
				code, detail := errcode.DecodeFramePayload(fr.Payload)
				msg := "download: " + code
				if detail != "" {
					msg = msg + " (" + detail + ")"
				}
				return mcpErrorResult(errcode.New(code, msg)), nil
			}
		}
	}
}

// statRemoteSize asks the daemon for the size of path. ok=false on any
// miss — missing file, non-file type, unexpected reply, transport
// error — because the caller only needs "large file worth
// parallelizing?"; the sequential path stays the authority that
// surfaces the real error to the user.
func (r *Router) statRemoteSize(ctx context.Context, path string) (int64, bool) {
	reqID := newReqID()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	payload, err := fsproto.StatRequest{Path: path}.Encode()
	if err != nil {
		return 0, false
	}
	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeStatRequest, ReqID: reqID, Payload: payload}); err != nil {
		return 0, false
	}
	select {
	case <-ctx.Done():
		return 0, false
	case f, ok := <-ch:
		if !ok {
			return 0, false
		}
		if f.Type != proto.FrameTypeStatResponse {
			return 0, false
		}
		resp, err := fsproto.DecodeStatResponse(f.Payload)
		if err != nil || resp.Err != "" || resp.Entry == nil || resp.Entry.Type != "file" {
			return 0, false
		}
		return resp.Entry.Size, true
	}
}

// segmentError formats a daemon-side segment failure like the other
// transfer paths do ("context (detail)" when a detail is present).
func segmentError(code, detail string) error {
	msg := "download segment: " + code
	if detail != "" {
		msg = msg + " (" + detail + ")"
	}
	return errors.New(msg)
}

// downloadParallel fetches the first size bytes of args.SrcPath as
// concurrent offset-range segments assembled straight into the dest
// temp file. The Router demultiplexes replies by reqID and sendMu
// serializes frame writes, so segments are ordinary concurrent
// DownloadRequests — no protocol or gateway change. WriteAt keeps the
// assembly order-independent; the whole-file sha256 is computed
// locally because each segment's FileComplete digest only covers its
// own range (a mid-transfer file rewrite can therefore produce a
// first-size-bytes snapshot; the sequential path has comparable
// best-effort semantics). On any error the temp file is removed, dest
// is left untouched, and the caller falls back to sequential.
func (r *Router) downloadParallel(ctx context.Context, args downloadArgs, size int64) (any, error) {
	tmp, err := os.CreateTemp(filepath.Dir(args.DestPath), "."+filepath.Base(args.DestPath)+".agbridge-*.tmp")
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}

	n := int(size / parallelSegmentTarget)
	if n < parallelSegmentsMin {
		n = parallelSegmentsMin
	}
	if n > parallelSegmentsMax {
		n = parallelSegmentsMax
	}
	base := size / int64(n)

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
		received atomic.Int64
	)
	fail := func(err error) {
		errOnce.Do(func() { firstErr = err })
	}
	emit := EmitterFromContext(ctx)

	for i := 0; i < n; i++ {
		off := int64(i) * base
		ln := base
		if i == n-1 {
			ln = size - off // last segment absorbs the division remainder
		}
		wg.Add(1)
		go func(off, ln int64) {
			defer wg.Done()
			if err := r.downloadSegment(ctx, args.SrcPath, off, ln, tmp, &received, emit); err != nil {
				fail(err)
			}
		}(off, ln)
	}
	wg.Wait()

	if firstErr != nil {
		cleanup()
		return nil, firstErr
	}

	// Whole-file digest over the assembled bytes (segments' own digests
	// are per-range and not composable).
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(tmp, 0, size)); err != nil {
		cleanup()
		return nil, err
	}
	sha := hex.EncodeToString(h.Sum(nil))

	if err := finalizeTempDownload(tmp, args.DestPath); err != nil {
		// finalizeTempDownload closes tmp on every error path; only the
		// leftover file needs removing.
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	meta := map[string]any{
		"size":      size,
		"sha256":    sha,
		"src_path":  args.SrcPath,
		"dest_path": args.DestPath,
		"segments":  n,
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text",
			"text": fmt.Sprintf("downloaded %d bytes to %s (sha256=%s, %d segments)", size, args.DestPath, sha, n)}},
		"_meta": meta,
	}, nil
}

// downloadSegment fetches [off, off+ln) of srcPath and places each
// chunk at its absolute offset with WriteAt. Completion is accepted
// only on a clean FileComplete whose byte count matches the request: a
// daemon that served fewer bytes (the file shrank mid-transfer) is an
// error, not a silently truncated result.
func (r *Router) downloadSegment(ctx context.Context, srcPath string, off, ln int64, tmp *os.File, received *atomic.Int64, emit ProgressEmitter) error {
	reqID := newReqID()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	reqJSON, _ := fileproto.DownloadRequest{SrcPath: srcPath, Offset: off, Limit: ln, Binary: true}.Encode()
	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeDownloadRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return err
	}
	var written int64
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case fr, ok := <-ch:
			if !ok {
				return errors.New("cliclient: connection reset during parallel download")
			}
			switch fr.Type {
			case proto.FrameTypeFileChunk, proto.FrameTypeFileChunkBin:
				c, err := decodeChunk(fr.Type, fr.Payload)
				if err != nil {
					return err
				}
				if len(c.Data) == 0 {
					continue // EOF sentinel
				}
				if written+int64(len(c.Data)) > ln {
					// Over-delivery would overwrite the neighbouring
					// segment's region — treat as protocol error.
					return fmt.Errorf("cliclient: segment %d..%d over-delivered %d bytes", off, off+ln, written+int64(len(c.Data)))
				}
				if _, err := tmp.WriteAt(c.Data, off+written); err != nil {
					return err
				}
				written += int64(len(c.Data))
				emit(float64(received.Add(int64(len(c.Data)))), nil, "")
			case proto.FrameTypeFileComplete:
				c, err := fileproto.DecodeFileComplete(fr.Payload)
				if err != nil {
					return err
				}
				if c.Err != "" {
					return segmentError(c.Err, c.ErrDetail)
				}
				if c.Size != written || written != ln {
					return fmt.Errorf("cliclient: segment %d..%d got %d bytes (complete says %d)", off, off+ln, written, c.Size)
				}
				return nil
			case proto.FrameTypeError:
				code, detail := errcode.DecodeFramePayload(fr.Payload)
				return segmentError(code, detail)
			}
		}
	}
}
