package cliclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/fsproto"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/searchproto"
)

type searchArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Regex           *bool  `json:"regex,omitempty"` // pointer to detect omitted
	Glob            string `json:"glob,omitempty"`
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`
	MaxFiles        int    `json:"max_files,omitempty"`
}

// SearchHandler runs a pure-Go file-content search on the daemon.
func (r *Router) SearchHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args searchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Pattern == "" {
		return mcpErrorResult(errcode.New("bad_payload", "pattern required")), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path required")), nil
	}
	// Default regex=true when not specified.
	regexMode := true
	if args.Regex != nil {
		regexMode = *args.Regex
	}
	req := searchproto.SearchRequest{
		Pattern: args.Pattern, Path: args.Path, Regex: regexMode,
		Glob: args.Glob, CaseInsensitive: args.CaseInsensitive,
		MaxResults: args.MaxResults, MaxFiles: args.MaxFiles,
	}
	reqID := newReqID()
	reqJSON, _ := req.Encode()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeSearchRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
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
		case proto.FrameTypeSearchResponse:
			resp, err := searchproto.DecodeSearchResponse(f.Payload)
			if err != nil {
				return mcpErrorResult(errcode.New("bad_payload", "search: "+err.Error())), nil
			}
			if resp.Err != "" {
				msg := "search: " + resp.Err
				if resp.ErrDetail != "" {
					msg = msg + " (" + resp.ErrDetail + ")"
				}
				return mcpErrorResult(errcode.New(resp.Err, msg)), nil
			}
			var b strings.Builder
			for _, m := range resp.Results {
				b.WriteString(fmt.Sprintf("%s:%d:%d: %s\n", m.File, m.Line, m.Column, m.Snippet))
			}
			if b.Len() == 0 {
				b.WriteString(fmt.Sprintf("(no matches in %d files)", resp.TotalFilesSearched))
			} else if resp.Truncated {
				b.WriteString(fmt.Sprintf("--- showing %d of %d matches (truncated) ---\n",
					len(resp.Results), resp.TotalMatched))
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": b.String()}},
				"_meta": map[string]any{
					"results":              resp.Results,
					"total_matched":        resp.TotalMatched,
					"total_files_searched": resp.TotalFilesSearched,
					"truncated":            resp.Truncated,
					"duration_ms":          resp.DurationMs,
				},
			}, nil
		case proto.FrameTypeError:
			code, detail := errcode.DecodeFramePayload(f.Payload)
			msg := "search: " + code
			if detail != "" {
				msg = msg + " (" + detail + ")"
			}
			return mcpErrorResult(errcode.New(code, msg)), nil
		}
		return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame %d", f.Type))), nil
	}
}

// ListDirectoryHandler returns non-recursive children of a directory on
// the daemon.
func (r *Router) ListDirectoryHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var args pathOnlyArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
	}
	if args.Path == "" {
		return mcpErrorResult(errcode.New("bad_payload", "path is required")), nil
	}
	reqID := newReqID()
	reqJSON, _ := fsproto.ListDirRequest{Path: args.Path}.Encode()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeListDirRequest, ReqID: reqID, Payload: reqJSON}); err != nil {
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
		case proto.FrameTypeListDirResponse:
			resp, err := fsproto.DecodeListDirResponse(f.Payload)
			if err != nil {
				return mcpErrorResult(errcode.New("bad_payload", "list_directory: "+err.Error())), nil
			}
			if resp.Err != "" {
				msg := "list_directory: " + resp.Err
				if resp.ErrDetail != "" {
					msg = msg + " (" + resp.ErrDetail + ")"
				}
				return mcpErrorResult(errcode.New(resp.Err, msg)), nil
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": renderListDir(resp)}},
				"_meta":   map[string]any{"entries": resp.Entries, "count": len(resp.Entries)},
			}, nil
		case proto.FrameTypeError:
			code, detail := errcode.DecodeFramePayload(f.Payload)
			msg := "list_directory: " + code
			if detail != "" {
				msg = msg + " (" + detail + ")"
			}
			return mcpErrorResult(errcode.New(code, msg)), nil
		}
		return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame %d", f.Type))), nil
	}
}

func renderListDir(resp fsproto.ListDirResponse) string {
	var b strings.Builder
	for _, e := range resp.Entries {
		b.WriteString(fmt.Sprintf("%-4s %10d %o %s\n", e.Type, e.Size, e.Mode, e.Name))
	}
	if b.Len() == 0 {
		return "(empty directory)"
	}
	return b.String()
}
