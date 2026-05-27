package cliclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/fileproto"
	
	"github.com/pw0rld/agbridge/internal/proto"
)

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
			if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Data: data, Eof: eof}); err != nil {
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
				if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Data: data, Eof: eof}); err != nil {
					return mcpErrorResult(errcode.New("network_lost", err.Error())), nil
				}
				if eof {
					break
				}
			}
			if rerr == io.EOF {
				// Zero-length file: send a single empty Eof chunk so daemon closes the write cleanly.
				if err := r.sendFileChunk(ctx, reqID, fileproto.FileChunk{Eof: true}); err != nil {
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
			c, _ := fileproto.DecodeFileComplete(f.Payload)
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
	reqID := newReqID()
	reqJSON, _ := fileproto.DownloadRequest{
		SrcPath: args.SrcPath, Offset: args.Offset, Limit: args.Limit,
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
	var (
		f       *os.File
		content []byte
	)
	if args.DestPath != "" {
		var err error
		f, err = os.Create(args.DestPath)
		if err != nil {
			return mcpErrorResult(errcode.New("write_failed", err.Error())), nil
		}
		defer f.Close()
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
			case proto.FrameTypeFileChunk:
				c, err := fileproto.DecodeFileChunk(fr.Payload)
				if err != nil {
					return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
				}
				if f != nil {
					if _, err := f.Write(c.Data); err != nil {
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
				c, _ := fileproto.DecodeFileComplete(fr.Payload)
				if c.Err != "" {
					msg := "download: " + c.Err
					if c.ErrDetail != "" {
						msg = msg + " (" + c.ErrDetail + ")"
					}
					return mcpErrorResult(errcode.New(c.Err, msg)), nil
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
