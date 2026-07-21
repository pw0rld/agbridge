package cliclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pw0rld/agbridge/internal/capabilities"
	"github.com/pw0rld/agbridge/internal/errcode"
	"github.com/pw0rld/agbridge/internal/proto"
)

// CapabilitiesHandler sends a FrameTypeCapabilitiesRequest to the daemon
// and returns the daemon's policy snapshot. The JSON payload is exposed
// verbatim under `_meta.capabilities` so AI agents can parse it, plus a
// human-readable text body.
func (r *Router) CapabilitiesHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	reqID := newReqID()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeCapabilitiesRequest, ReqID: reqID}); err != nil {
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
		case proto.FrameTypeCapabilitiesResponse:
			caps, err := capabilities.Decode(f.Payload)
			if err != nil {
				return mcpErrorResult(errcode.New("bad_payload", err.Error())), nil
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": renderCapabilities(caps)}},
				"_meta":   map[string]any{"capabilities": caps},
			}, nil
		case proto.FrameTypeError:
			code, detail := errcode.DecodeFramePayload(f.Payload)
			msg := "capabilities: " + code
			if detail != "" {
				msg = msg + " (" + detail + ")"
			}
			return mcpErrorResult(errcode.New(code, msg)), nil
		}
		return mcpErrorResult(errcode.New("protocol_error", fmt.Sprintf("unexpected frame type %d", f.Type))), nil
	}
}

func renderCapabilities(c capabilities.Capabilities) string {
	return fmt.Sprintf(
		"daemon=%s hostname=%s version=%s\nexec_enabled=%v\ndefault_working_dir=%s\nenv_allowlist=%v\nforbidden_ports=%v\nmax_file_bytes=%d",
		c.DaemonName, c.DaemonHostname, c.AgbridgeVersion,
		c.ExecEnabled,
		c.DefaultWorkingDir,
		c.EnvAllowlist, c.ForbiddenPorts, c.MaxFileBytes,
	)
}
