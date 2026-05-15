package gateway

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/proto"
)

// fakeConn satisfies connIO so SessionTable tests don't need a real wss conn.
type fakeConn struct {
	closed atomic.Bool
}

func (f *fakeConn) Recv(ctx context.Context) (proto.Frame, error) { return proto.Frame{}, nil }
func (f *fakeConn) Send(ctx context.Context, _ proto.Frame) error { return nil }
func (f *fakeConn) Close() error                                  { f.closed.Store(true); return nil }
func (f *fakeConn) IsClosed() bool                                { return f.closed.Load() }

func TestSessionTableRevokeClosesUnauthorized(t *testing.T) {
	cr := NewCredRegistry(sampleCfg())
	st := NewSessionTable()

	connAlpha := &fakeConn{}
	connBravo := &fakeConn{}
	connLab1 := &fakeConn{}

	_, rA := st.Register(&Session{Role: "bridge", Name: "alpha", KeyHash: "sha256:a", Conn: connAlpha})
	defer rA()
	_, rB := st.Register(&Session{Role: "bridge", Name: "bravo", KeyHash: "sha256:b", Conn: connBravo})
	defer rB()
	_, rL := st.Register(&Session{Role: "daemon", Name: "lab1", KeyHash: "sha256:x", Conn: connLab1})
	defer rL()

	// Replace removes bravo entirely.
	newCfg := &config.GatewayConfig{
		Agents: []config.AgentEntry{
			{Name: "alpha", APIKeyHash: "sha256:a"},
		},
		Daemons: []config.DaemonEntry{
			{Name: "lab1", TokenHash: "sha256:x"},
		},
	}
	cr.Replace(newCfg)
	closed := st.Revoke(cr)
	if len(closed) != 1 || closed[0] != "bridge/bravo" {
		t.Errorf("closed = %v, want [bridge/bravo]", closed)
	}
	if !connBravo.IsClosed() {
		t.Errorf("bravo conn not closed")
	}
	if connAlpha.IsClosed() {
		t.Errorf("alpha conn closed but should have been kept")
	}
	if connLab1.IsClosed() {
		t.Errorf("lab1 conn closed but should have been kept")
	}
}

func TestSessionTableRevokeOnRotatedHash(t *testing.T) {
	cr := NewCredRegistry(sampleCfg())
	st := NewSessionTable()

	conn := &fakeConn{}
	_, rel := st.Register(&Session{Role: "bridge", Name: "alpha", KeyHash: "sha256:a", Conn: conn})
	defer rel()

	// Rotate alpha's APIKeyHash. The session was minted under the old key
	// so it should be revoked.
	cr.Replace(&config.GatewayConfig{
		Agents: []config.AgentEntry{
			{Name: "alpha", APIKeyHash: "sha256:NEW"},
		},
	})
	closed := st.Revoke(cr)
	if len(closed) != 1 {
		t.Fatalf("closed = %v, want one entry", closed)
	}
	if !conn.IsClosed() {
		t.Errorf("alpha conn not closed after key rotation")
	}
}

func TestSessionTableRegisterReleaseFreesSlot(t *testing.T) {
	st := NewSessionTable()
	conn := &fakeConn{}
	_, rel := st.Register(&Session{Role: "daemon", Name: "lab1", KeyHash: "h", Conn: conn})
	rel()
	if st.Count() != 0 {
		t.Errorf("Count = %d after release, want 0", st.Count())
	}
}
