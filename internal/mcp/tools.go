// Package mcp is a residual stub. v0.0.1 carried a full MCP stdio server
// here; v0.0.2 deletes that server (the agbridge surface is CLI-only now).
//
// All that remains is the ProgressEmitter context plumbing, which the
// cliclient handlers still call through. With CLI invocation there is no
// progress sink wired into the context, so EmitterFromContext returns a
// no-op — handlers can keep emit() calls in place unconditionally.
//
// The package is kept (rather than folded into cliclient) to avoid editing
// cliclient in Phase 4's already-large deletion. A follow-up may rename
// and relocate this shim.
package mcp

import "context"

// ProgressEmitter writes one progress event. Total is optional (use nil
// when the work has no known upper bound — e.g. exec output streaming).
// Message is shown to the user; keep it short.
//
// In v0.0.2 the CLI client never installs a real emitter, so this type
// exists purely to keep the handler call-sites compiling.
type ProgressEmitter func(progress float64, total *float64, message string)

type progressEmitterKey struct{}

// EmitterFromContext returns the progress emitter installed for the current
// call, or a no-op emitter when none is installed. Handlers can call the
// returned function unconditionally.
func EmitterFromContext(ctx context.Context) ProgressEmitter {
	if e, ok := ctx.Value(progressEmitterKey{}).(ProgressEmitter); ok && e != nil {
		return e
	}
	return func(_ float64, _ *float64, _ string) {}
}
