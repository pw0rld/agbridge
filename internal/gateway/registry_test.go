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
	p := NewDaemonProxy(c)
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
	_ = r.Register("lab01", NewDaemonProxy(&stubConn{}))
	if err := r.Register("lab01", NewDaemonProxy(&stubConn{})); err == nil {
		t.Errorf("expected duplicate-registration error")
	}
}

func TestDaemonProxyDropsWhenNoWriter(t *testing.T) {
	p := NewDaemonProxy(&stubConn{})
	if err := p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing}); err != nil {
		t.Errorf("Deliver with no writer should be a no-op, got %v", err)
	}
}

func TestDaemonProxySetAndClearWriter(t *testing.T) {
	p := NewDaemonProxy(&stubConn{})
	delivered := 0
	p.SetWriter(func(_ context.Context, _ proto.Frame) error { delivered++; return nil })
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing})
	if delivered != 1 {
		t.Errorf("delivered = %d, want 1", delivered)
	}
	p.SetWriter(nil)
	_ = p.Deliver(context.Background(), proto.Frame{Type: proto.FrameTypePing})
	if delivered != 1 {
		t.Errorf("delivered = %d after clear, want 1", delivered)
	}
}
