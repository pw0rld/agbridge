package cliclient

import "context"

// ProgressEmitter writes one progress event. Total is optional (use nil
// when the work has no known upper bound — e.g. exec output streaming).
// Message is shown to the user; keep it short.
//
// In v0.0.2 the CLI client never installs a real emitter, so calling the
// returned function from EmitterFromContext is a no-op by default.
// Handler code can call emit() unconditionally; the noop hot path is one
// indirect-call cheap. A future caller (e.g. an in-process daemon mode)
// can install an emitter via WithEmitter to receive progress events.
type ProgressEmitter func(progress float64, total *float64, message string)

type progressEmitterKey struct{}

// EmitterFromContext returns the progress emitter installed for the current
// call, or a no-op emitter when none is installed.
func EmitterFromContext(ctx context.Context) ProgressEmitter {
	if e, ok := ctx.Value(progressEmitterKey{}).(ProgressEmitter); ok && e != nil {
		return e
	}
	return func(_ float64, _ *float64, _ string) {}
}

// WithEmitter installs e as the progress emitter for ctx. Currently
// unused by the CLI itself; reserved for future progress-aware callers.
func WithEmitter(ctx context.Context, e ProgressEmitter) context.Context {
	return context.WithValue(ctx, progressEmitterKey{}, e)
}
