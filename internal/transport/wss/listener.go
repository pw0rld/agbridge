package wss

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/transport"
)

// Listener wraps a TLS listener + HTTP server doing websocket upgrade.
// Each accepted websocket conn becomes a *Conn delivered via Accept().
type Listener struct {
	tlsLn     net.Listener
	httpSrv   *http.Server
	conns     chan *Conn
	closeCh   chan struct{}
	closeOnce sync.Once
}

// Listen starts a TLS:port listener that upgrades incoming HTTP to WSS.
func Listen(ctx context.Context, addr string, tlsCfg *tls.Config) (*Listener, error) {
	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = tls.VersionTLS13
	}
	ln, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, err
	}
	l := &Listener{
		tlsLn:   ln,
		conns:   make(chan *Conn, 8),
		closeCh: make(chan struct{}),
	}
	up := websocket.Upgrader{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Identity is filled in Phase 2 (auth handshake). Phase 1 leaves blank.
		c := NewConn(ws, transport.Identity{})
		select {
		case l.conns <- c:
		case <-l.closeCh:
			_ = ws.Close()
		}
	})
	l.httpSrv = &http.Server{Handler: mux}
	go l.httpSrv.Serve(ln)
	return l, nil
}

func (l *Listener) Accept(ctx context.Context) (*Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.closeCh:
		return nil, net.ErrClosed
	}
}

func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closeCh)
		_ = l.httpSrv.Close()
	})
	return nil
}

func (l *Listener) Addr() net.Addr { return l.tlsLn.Addr() }
