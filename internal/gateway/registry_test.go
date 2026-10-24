package gateway

import (
	"context"
	"testing"

	"github.com/pw0rld/agbridge/internal/proto"
)

// stubConn implements connIO + Close for registry testing.
type stubConn struct {
	closed bool
}

func (s *stubConn) Send(context.Context, proto.Frame) error   { return nil }
func (s *stubConn) Recv(context.Context) (proto.Frame, error) { return proto.Frame{}, nil }
func (s *stubConn) Close() error                              { s.closed = true; return nil }

func TestRegistryRegisterLookupUnregister(t *testing.T) {
	r := NewRegistry()
	c := &stubConn{}
	p := NewDaemonProxy(c, 0)
	if err := r.Register("lab01", p); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, ok := r.Lookup("lab01")
	if !ok || got != p {
		t.Errorf("lookup: got %v ok=%v", got, ok)
	}
	r.Unregister("lab01")
	if _, ok := r.Lookup("lab01"); ok {
		t.Errorf("still present after unregister")
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	_ = r.Register("lab01", NewDaemonProxy(&stubConn{}, 0))
	if err := r.Register("lab01", NewDaemonProxy(&stubConn{}, 0)); err == nil {
		t.Errorf("expected duplicate-registration error")
	}
}

func TestDaemonProxyDropsWhenNoWriter(t *testing.T) {
	p := NewDaemonProxy(&stubConn{}, 0)
	if err := p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing}); err != nil {
		t.Errorf("Deliver with no writer should be a no-op, got %v", err)
	}
}

func TestDaemonProxySubscribeAndUnsubscribe(t *testing.T) {
	p := NewDaemonProxy(&stubConn{}, 0)
	delivered := 0
	sub := p.Subscribe("agent-a", func(_ context.Context, _ proto.Frame) error { delivered++; return nil })
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing})
	if delivered != 1 {
		t.Errorf("delivered = %d, want 1", delivered)
	}
	p.Unsubscribe(sub)
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing})
	if delivered != 1 {
		t.Errorf("delivered = %d after unsubscribe, want 1", delivered)
	}
}

// TestDaemonProxyUnsubscribeDoesNotEvictOthers pins the writer-slot
// regression: a stale bridge's Unsubscribe must never stop delivery to a
// subscription that is still active (the old single-slot SetWriter(nil)
// cleared whoever happened to be installed).
func TestDaemonProxyUnsubscribeDoesNotEvictOthers(t *testing.T) {
	p := NewDaemonProxy(&stubConn{}, 0)
	old := p.Subscribe("agent-a", func(context.Context, proto.Frame) error { return nil })
	got := 0
	p.Subscribe("agent-a", func(_ context.Context, _ proto.Frame) error { got++; return nil })
	p.Unsubscribe(old) // old bridge tearing down AFTER the replacement subscribed
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePong})
	if got != 1 {
		t.Errorf("replacement subscription evicted: delivered = %d, want 1", got)
	}
}

// TestDaemonProxyDeadSubscriptionPruned: a writer whose Send errors is
// removed automatically on the next Deliver, without waiting for its
// handleBridge defer.
func TestDaemonProxyDeadSubscriptionPruned(t *testing.T) {
	p := NewDaemonProxy(&stubConn{}, 0)
	calls := 0
	p.Subscribe("dead", func(context.Context, proto.Frame) error { calls++; return context.Canceled })
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing})
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing})
	if calls != 1 {
		t.Errorf("dead writer retried: calls = %d, want 1", calls)
	}
}
