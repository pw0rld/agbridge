package cliclient

import (
	"context"
	"encoding/json"
	"fmt"
)

// Dispatch invokes a tool handler by its public name. It is the single
// registry of CLI-reachable handlers: the agent broker dispatches
// cross-process calls through it, and InvokeHandler uses it for the
// one-shot fallback, so both paths execute byte-identical handler code.
//
// Handler names mirror the MCP tool names used in docs and the removed
// stdio bridge.
func (r *Router) Dispatch(ctx context.Context, name string, args json.RawMessage) (any, error) {
	switch name {
	case "exec":
		return r.ExecHandler(ctx, args)
	case "read_file":
		return r.ReadFileHandler(ctx, args)
	case "write_file":
		return r.WriteFileHandler(ctx, args)
	case "edit_file":
		return r.EditFileHandler(ctx, args)
	case "stat":
		return r.StatHandler(ctx, args)
	case "list_dir":
		return r.ListDirectoryHandler(ctx, args)
	case "search":
		return r.SearchHandler(ctx, args)
	case "upload":
		return r.UploadHandler(ctx, args)
	case "download":
		return r.DownloadHandler(ctx, args)
	case "capabilities":
		return r.CapabilitiesHandler(ctx, args)
	default:
		return nil, fmt.Errorf("unknown_handler: %s", name)
	}
}

// ClientDispatch lets the agent pool call through a Client without
// reaching for Router().
func (c *Client) Dispatch(ctx context.Context, name string, args json.RawMessage) (any, error) {
	return c.Router().Dispatch(ctx, name, args)
}
