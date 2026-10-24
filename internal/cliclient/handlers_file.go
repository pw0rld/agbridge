package cliclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/fsproto"

	"github.com/pw0rld/agbridge/internal/proto"
)

type readFileArgs struct {
	Path    string `json:"path"`
	MaxSize int    `json:"max_size"`
	Offset  int64  `json:"offset"`
	Limit   int64  `json:"limit"`
}

// ReadFileHandler reads a file from the daemon and returns its content
// (inline text or base64 fallback) plus size + sha256. 10 MB cap.
func (r *Router) ReadFileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args readFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	reqID := newReqID()
	// Binary=true is always requested: an old daemon ignores the unknown
	// field and replies with JSON chunks, which the loop below accepts
	// too — no capabilities round-trip needed on receive paths.
	reqJSON, _ := fileproto.FileReadRequest{
		Path: args.Path, MaxSize: args.MaxSize, Offset: args.Offset, Limit: args.Limit, Binary: true,
	}.Encode()

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileReadRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	emit := EmitterFromContext(ctx)
	var bytesReceived int64
	var content []byte
	for {
		select {
		case <-ctx.Done():
			return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
		case f, ok := <-ch:
			if !ok {
				return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
			}
			switch f.Type {
			case proto.FrameTypeFileChunk, proto.FrameTypeFileChunkBin:
				c, err := decodeChunk(f.Type, f.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				if int64(len(content))+int64(len(c.Data)) > maxBufferedOutput {
					return mcpErrorResult(errcode.New("exceeds_max_size", "file exceeds 10 MB cap")), nil
				}
				content = append(content, c.Data...)
				// Skip emit for EOF sentinels (Data==nil, Eof==true); only data-bearing chunks advance progress.
				if len(c.Data) > 0 {
					bytesReceived += int64(len(c.Data))
					emit(float64(bytesReceived), nil, "")
				}
			case proto.FrameTypeFileComplete:
				c, err := fileproto.DecodeFileComplete(f.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", "read_file: "+err.Error())), nil
				}
				return buildReadFileResult(content, c), nil
			case proto.FrameTypeError:
				return mcpErrorResult(errcode.New(string(f.Payload), "daemon error")), nil
			}
		}
	}
}

func buildReadFileResult(content []byte, c fileproto.FileComplete) any {
	if c.Err != "" {
		msg := "read_file: " + c.Err
		if c.ErrDetail != "" {
			msg = msg + " (" + c.ErrDetail + ")"
		}
		return mcpErrorResult(errcode.New(c.Err, msg))
	}
	meta := map[string]any{
		"size":   c.Size,
		"sha256": c.Sha256,
	}
	var text string
	if utf8.Valid(content) {
		text = string(content)
	} else {
		// Binary file: emit base64 only when not UTF-8.
		text = fmt.Sprintf("(binary %d bytes, sha256=%s)", c.Size, c.Sha256)
		meta["content_b64"] = base64.StdEncoding.EncodeToString(content)
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"_meta":   meta,
	}
}

type writeFileArgs struct {
	Path       string  `json:"path"`
	Content    *string `json:"content"`
	ContentB64 *string `json:"content_b64"`
	Mode       uint32  `json:"mode"`
}

const writeChunkSize = 64 * 1024

// WriteFileHandler writes a file on the daemon, accepting either raw
// `content` (recommended for text) or base64-encoded `content_b64`.
// Exactly one must be set. 10 MB cap.
func (r *Router) WriteFileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args writeFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	hasContent := args.Content != nil
	hasB64 := args.ContentB64 != nil
	if hasContent && hasB64 {
		return mcpErrorResult(errcode.New("bad_payload", "only one of content / content_b64 may be set")), nil
	}
	if !hasContent && !hasB64 {
		return mcpErrorResult(errcode.New("bad_payload", "content or content_b64 required")), nil
	}
	var content []byte
	if hasContent {
		content = []byte(*args.Content)
	} else {
		var err error
		content, err = base64.StdEncoding.DecodeString(*args.ContentB64)
		if err != nil {
			return mcpErrorResult(errcode.New("bad_payload", "content_b64: "+err.Error())), nil
		}
	}
	if int64(len(content)) > maxBufferedOutput {
		return mcpErrorResult(errcode.New("exceeds_max_size", "content exceeds 10 MB cap")), nil
	}

	reqID := newReqID()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	// We are the chunk sender: only send binary chunks when the daemon
	// advertises support (an old daemon would silently drop them).
	binary := r.supportsBinaryChunks(ctx)

	reqJSON, _ := fileproto.FileWriteRequest{Path: args.Path, Mode: args.Mode}.Encode()
	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileWriteRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}

	if len(content) == 0 {
		if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Eof: true}, binary); err != nil {
			return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
		}
	} else {
		for off := 0; off < len(content); off += writeChunkSize {
			end := off + writeChunkSize
			if end > len(content) {
				end = len(content)
			}
			chunk := fileproto.FileChunk{Data: content[off:end], Eof: end == len(content)}
			if err := r.sendFileChunk(ctx, reqID, chunk, binary); err != nil {
				return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
			}
		}
	}

	for {
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
					return mcpErrorResult(errcode.New("bad_payload", "write_file: "+err.Error())), nil
				}
				if c.Err != "" {
					msg := "write_file: " + c.Err
					if c.ErrDetail != "" {
						msg = msg + " (" + c.ErrDetail + ")"
					}
					return mcpErrorResult(errcode.New(c.Err, msg)), nil
				}
				return map[string]any{
					"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("wrote %d bytes (sha256=%s)", c.Size, c.Sha256)}},
					"_meta": map[string]any{
						"size":          c.Size,
						"bytes_written": c.Size,
						"sha256":        c.Sha256,
					},
				}, nil
			case proto.FrameTypeError:
				return mcpErrorResult(errcode.New(string(f.Payload), "daemon error")), nil
			}
		}
	}
}

// sendFileChunk wraps fileproto.FileChunk into a chunk frame and ships
// it via sendInner. Used by write_file and upload. binary selects the
// compact FrameTypeFileChunkBin encoding; callers must only pass true
// after supportsBinaryChunks confirmed the daemon understands it.
func (r *Router) sendFileChunk(ctx context.Context, reqID string, c fileproto.FileChunk, binary bool) error {
	if binary {
		return r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileChunkBin, ReqID: reqID, Payload: fileproto.EncodeChunkBinary(c)})
	}
	payload, _ := c.Encode()
	return r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileChunk, ReqID: reqID, Payload: payload})
}

// decodeChunk decodes a chunk frame payload according to its frame
// type: JSON+base64 for FrameTypeFileChunk, raw bytes for
// FrameTypeFileChunkBin. Receivers use this to accept both encodings.
func decodeChunk(typ proto.FrameType, payload []byte) (fileproto.FileChunk, error) {
	if typ == proto.FrameTypeFileChunkBin {
		return fileproto.DecodeChunkBinary(payload)
	}
	return fileproto.DecodeFileChunk(payload)
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// EditFileHandler replaces old_string with new_string in the named file
// atomically. By default old_string must occur exactly once.
func (r *Router) EditFileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args editFileArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	if args.OldString == "" {
		return mcpErrorResult(errcode.New("bad_payload", "old_string is required and must not be empty")), nil
	}
	reqID := newReqID()
	reqJSON, _ := fileproto.FileEditRequest{
		Path: args.Path, OldString: args.OldString, NewString: args.NewString, ReplaceAll: args.ReplaceAll,
	}.Encode()

	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileEditRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
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
				return mcpErrorResult(errcode.New("bad_payload", "edit_file: "+err.Error())), nil
			}
			if c.Err != "" {
				msg := "edit_file: " + c.Err
				if c.ErrDetail != "" {
					msg = msg + " (" + c.ErrDetail + ")"
				}
				return mcpErrorResult(errcode.New(c.Err, msg)), nil
			}
			text := fmt.Sprintf("edited %d bytes (sha256=%s)", c.Size, c.Sha256)
			if c.BackupPath != "" {
				text += "\nbackup: " + c.BackupPath
			}
			meta := map[string]any{
				"size":   c.Size,
				"sha256": c.Sha256,
			}
			if c.BackupPath != "" {
				meta["backup_path"] = c.BackupPath
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": text}},
				"_meta":   meta,
			}, nil
		case proto.FrameTypeError:
			code, detail := errcode.DecodeFramePayload(f.Payload)
			msg := "edit_file: " + code
			if detail != "" {
				msg = msg + " (" + detail + ")"
			}
			return mcpErrorResult(errcode.New(code, msg)), nil
		}
		return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame %d", f.Type))), nil
	}
}

type pathOnlyArgs struct {
	Path string `json:"path"`
}

// StatHandler returns metadata for a single path on the daemon.
// Args accept an optional follow=true to resolve a final symlink
// (used by `cd` so symlinked directories pass the is-dir check).
func (r *Router) StatHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Path   string `json:"path"`
		Follow bool   `json:"follow,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	reqID := newReqID()
	reqJSON, _ := fsproto.StatRequest{Path: args.Path, Follow: args.Follow}.Encode()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeStatRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
		return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
	}
	select {
	case <-ctx.Done():
		return mcpErrorResult(errcode.New("network_lost", "context cancelled")), nil
	case f, ok := <-ch:
		if !ok {
			return mcpErrorResult(errcode.New("network_lost", "connection reset")), nil
		}
		switch f.Type {
		case proto.FrameTypeStatResponse:
			resp, err := fsproto.DecodeStatResponse(f.Payload)
			if err != nil {
				return mcpErrorResult(errcode.New("bad_payload", "stat: "+err.Error())), nil
			}
			if resp.Err != "" {
				msg := "stat: " + resp.Err
				if resp.ErrDetail != "" {
					msg = msg + " (" + resp.ErrDetail + ")"
				}
				return mcpErrorResult(errcode.New(resp.Err, msg)), nil
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf(
					"name=%s type=%s size=%d mode=%o mod_time=%d",
					resp.Entry.Name, resp.Entry.Type, resp.Entry.Size, resp.Entry.Mode, resp.Entry.ModTime,
				)}},
				"_meta": map[string]any{"entry": resp.Entry},
			}, nil
		case proto.FrameTypeError:
			code, detail := errcode.DecodeFramePayload(f.Payload)
			msg := "stat: " + code
			if detail != "" {
				msg = msg + " (" + detail + ")"
			}
			return mcpErrorResult(errcode.New(code, msg)), nil
		}
		return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame %d", f.Type))), nil
	}
}
