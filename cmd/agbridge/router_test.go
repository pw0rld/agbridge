package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/proto"
)

func newRouterForTest(t *testing.T) *router {
	t.Helper()
	r, err := newRouter(context.Background(), nil, []byte("k"), &config.BridgeConfig{E2EMode: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRouterReplaceConnClosesPending(t *testing.T) {
	r := newRouterForTest(t)
	ch1 := r.registerCall("req1")
	ch2 := r.registerCall("req2")
	r.replaceConn(nil)
	for name, ch := range map[string]<-chan any{"ch1": readable(ch1), "ch2": readable(ch2)} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("%s yielded a value, expected closed channel", name)
			}
		case <-time.After(time.Second):
			t.Errorf("%s: replaceConn did not close pending channel", name)
		}
	}
	r.mu.Lock()
	n := len(r.pending)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("pending map size = %d, want 0", n)
	}
}

func TestRouterReplaceConnSwapsActiveConn(t *testing.T) {
	r := newRouterForTest(t)
	if got := r.currentConn(); got != nil {
		t.Fatalf("expected nil initial conn, got %v", got)
	}
	r.replaceConn(nil)
	if got := r.currentConn(); got != nil {
		t.Fatalf("currentConn = %v, want nil", got)
	}
}

// TestRouterMarkIfEvictedOnSessionReplaced verifies F7: an Error frame
// with payload "session_replaced" flips the router's evicted flag so the
// supervisor knows to exit rather than reconnect.
func TestRouterMarkIfEvictedOnSessionReplaced(t *testing.T) {
	r := newRouterForTest(t)
	if r.wasEvicted() {
		t.Fatal("evicted flag should be false on fresh router")
	}
	matched := r.markIfEvicted(proto.Frame{
		Type:    proto.FrameTypeError,
		Payload: []byte("session_replaced"),
	})
	if !matched {
		t.Errorf("markIfEvicted should return true for session_replaced frame")
	}
	if !r.wasEvicted() {
		t.Errorf("wasEvicted should be true after session_replaced frame")
	}
}

// TestRouterMarkIfEvictedIgnoresOtherFrames ensures we only react to the
// specific eviction signal — other Error payloads or Route frames must
// leave the flag untouched (otherwise we'd mistake daemon_offline for an
// eviction and refuse to reconnect).
func TestRouterMarkIfEvictedIgnoresOtherFrames(t *testing.T) {
	r := newRouterForTest(t)
	cases := []proto.Frame{
		{Type: proto.FrameTypeError, Payload: []byte("daemon_offline")},
		{Type: proto.FrameTypeError, Payload: []byte("bad_mac")},
		{Type: proto.FrameTypeRoute, Payload: []byte("anything")},
		{Type: proto.FrameTypeHelloAck},
	}
	for _, f := range cases {
		if r.markIfEvicted(f) {
			t.Errorf("markIfEvicted should return false for %v / %q", f.Type, string(f.Payload))
		}
		if r.wasEvicted() {
			t.Errorf("evicted flag flipped on %v / %q", f.Type, string(f.Payload))
		}
	}
}

// TestSuperviseBridgeConnExitsWhenEvicted is F7's contract: once the
// router has been told "session_replaced", the supervisor must return
// errEvicted instead of looping back into dialer.Loop. Reconnecting in
// this state would just kick the newer bridge out again.
func TestSuperviseBridgeConnExitsWhenEvicted(t *testing.T) {
	r := newRouterForTest(t) // nil conn, so runReader returns immediately
	r.evicted.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := &config.BridgeConfig{E2EMode: "disabled"}
	err := superviseBridgeConn(ctx, r, cfg, nil)
	if !errors.Is(err, errEvicted) {
		t.Errorf("expected errEvicted, got %v", err)
	}
}

// TestSuperviseBridgeConnReturnsOnCtxCancel ensures a normal shutdown
// (ctx cancelled) returns the ctx error rather than entering a futile
// reconnect loop.
func TestSuperviseBridgeConnReturnsOnCtxCancel(t *testing.T) {
	r := newRouterForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &config.BridgeConfig{E2EMode: "disabled"}
	err := superviseBridgeConn(ctx, r, cfg, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// readable adapts <-chan proto.Frame to <-chan any without import churn in
// the table above.
func readable[T any](ch <-chan T) <-chan any {
	out := make(chan any)
	go func() {
		defer close(out)
		for v := range ch {
			out <- v
		}
	}()
	return out
}
