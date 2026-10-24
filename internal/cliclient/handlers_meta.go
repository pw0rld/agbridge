package cliclient

import (
	"context"
	"encoding/json"
	"errors"
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

// supportsBinaryChunks reports whether the daemon advertises
// capabilities.BinaryChunks, i.e. accepts FrameTypeFileChunkBin. Only
// senders of chunk frames (write_file/upload) need to gate on it: a
// daemon predating the flag silently drops unknown frame types, so
// blind binary chunks would hang the transfer. Receivers never need
// it — they accept both encodings and old daemons ignore the request's
// binary flag. A definitive answer (either way) is cached for the
// Router's lifetime; transport failures are not cached so a later
// transfer retries.
func (r *Router) supportsBinaryChunks(ctx context.Context) bool {
	r.capsMu.Lock()
	defer r.capsMu.Unlock()
	if r.capsBinaryChunks != nil {
		return *r.capsBinaryChunks
	}
	caps, err := r.fetchCapabilities(ctx)
	if err != nil {
		return false
	}
	v := caps.BinaryChunks
	r.capsBinaryChunks = &v
	return v
}

// fetchCapabilities performs one CapabilitiesRequest round-trip and
// decodes the reply.
func (r *Router) fetchCapabilities(ctx context.Context) (capabilities.Capabilities, error) {
	reqID := newReqID()
	ch := r.registerCall(reqID)
	defer r.unregisterCall(reqID)

	if err := r.sendInner(ctx, proto.Frame{Type: proto.FrameTypeCapabilitiesRequest, ReqID: reqID}); err != nil {
		return capabilities.Capabilities{}, err
	}
	select {
	case <-ctx.Done():
		return capabilities.Capabilities{}, context.Cause(ctx)
	case f, ok := <-ch:
		if !ok {
			return capabilities.Capabilities{}, errors.New("cliclient: connection reset during capabilities probe")
		}
		if f.Type != proto.FrameTypeCapabilitiesResponse {
			return capabilities.Capabilities{}, fmt.Errorf("cliclient: unexpected capabilities reply frame %d", f.Type)
		}
		return capabilities.Decode(f.Payload)
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
