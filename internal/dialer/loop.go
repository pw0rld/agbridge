// Package dialer provides an exponential-backoff retry loop that the bridge
// and daemon wrap around their WSS dial+handshake to survive transient
// network failures.
package dialer

import (
	"context"
	"math/rand"
	"time"
)

// Options controls Loop's retry cadence. Zero-value Options yields:
// Base=500ms, Cap=30s, Jitter=0.2 (±20%).
type Options struct {
	Base   time.Duration
	Cap    time.Duration
	Jitter float64 // fraction of the delay used as ±jitter; 0 disables

	// sleep is a seam for tests. Defaults to a context-aware sleep when nil.
	sleep func(time.Duration)
	// rand is a seam for tests. Defaults to a package-private prng when nil.
	rand func() float64
}

// Loop calls dial repeatedly until it returns nil, or ctx is cancelled.
// After each failure it waits min(Cap, Base * 2^attempt) ± Jitter%; the
// attempt counter is reset to zero on every call into Loop (callers wrap a
// single dial-and-run cycle and call Loop again on disconnect).
//
// Returns nil on dial success, or ctx.Err() on cancel.
func Loop(ctx context.Context, dial func(context.Context) error, opts Options) error {
	o := withDefaults(opts)
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := dial(ctx); err == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		delay := o.delayFor(attempt)
		if o.sleep != nil {
			o.sleep(delay)
			continue
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func withDefaults(o Options) Options {
	if o.Base <= 0 {
		o.Base = 500 * time.Millisecond
	}
	if o.Cap <= 0 {
		o.Cap = 30 * time.Second
	}
	if o.rand == nil {
		o.rand = defaultRand
	}
	return o
}

func (o Options) delayFor(attempt int) time.Duration {
	d := o.Base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= o.Cap {
			d = o.Cap
			break
		}
	}
	if d > o.Cap {
		d = o.Cap
	}
	if o.Jitter > 0 {
		// uniform in [-Jitter, +Jitter]
		f := (o.rand()*2 - 1) * o.Jitter
		d = time.Duration(float64(d) * (1 + f))
		if d < 0 {
			d = 0
		}
	}
	return d
}

var pkgRand = rand.New(rand.NewSource(time.Now().UnixNano()))

func defaultRand() float64 { return pkgRand.Float64() }
