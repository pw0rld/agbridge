// Package transport abstracts the bidirectional frame-passing substrate
// between two endpoints. MVP only ships transport/wss; future modes
// (mesh / QUIC / frp) implement the same interface.
package transport

import (
	"context"
	"crypto/tls"

	"github.com/pw0rld/agbridge/internal/proto"
)

// Credentials carries handshake material for Dial. Concrete fields are
// populated by Phase 2 (auth) — Phase 1 only uses an empty Credentials{}.
type Credentials struct {
	APIKey            string // bridge → gateway
	RegistrationToken string // daemon → gateway
	CertPinSHA256     []byte // optional pinned server cert fingerprint
}

// Identity is what a peer authenticates as on the other side. Phase 1
// populates only Role; auth payload comes in Phase 2.
type Identity struct {
	Role string // "bridge" | "daemon" | "gateway"
	Name string // logical name, e.g. "claude-laptop"
}

// Transport opens dial / listen channels. Implementations must be safe
// for concurrent use of Dial.
type Transport interface {
	Dial(ctx context.Context, endpoint string, creds Credentials) (Conn, error)
	Listen(ctx context.Context, addr string, tlsCfg *tls.Config) (Listener, error)
	Name() string
}

// Conn is a bidirectional frame channel. Send and Recv may be called
// concurrently (Send concurrent-safe; Recv from a single goroutine).
type Conn interface {
	Send(ctx context.Context, frame proto.Frame) error
	Recv(ctx context.Context) (proto.Frame, error)
	Close() error
	PeerIdentity() Identity
}

// Listener accepts incoming Conns.
type Listener interface {
	Accept(ctx context.Context) (Conn, error)
	Close() error
}
