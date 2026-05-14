package wss

import (
	"context"
	"crypto/tls"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/transport"
)

// Dial opens a TLS+WSS connection to endpoint and returns a *Conn.
// Credentials are reserved for Phase 2 auth and ignored here.
func Dial(ctx context.Context, endpoint string, _ transport.Credentials, tlsCfg *tls.Config) (*Conn, error) {
	if tlsCfg.MinVersion == 0 {
		tlsCfg = tlsCfg.Clone()
		tlsCfg.MinVersion = tls.VersionTLS13
	}
	dialer := websocket.Dialer{TLSClientConfig: tlsCfg}
	ws, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return NewConn(ws, transport.Identity{Role: "client"}), nil
}
