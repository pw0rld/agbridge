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
	if err := r.Register("lab01", c); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, ok := r.Lookup("lab01")
	if !ok || got != c {
		t.Errorf("lookup: got %v ok=%v", got, ok)
	}
	r.Unregister("lab01")
	if _, ok := r.Lookup("lab01"); ok {
		t.Errorf("still present after unregister")
	}
}

func TestRegistryRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	_ = r.Register("lab01", &stubConn{})
	if err := r.Register("lab01", &stubConn{}); err == nil {
		t.Errorf("expected duplicate-registration error")
	}
}
