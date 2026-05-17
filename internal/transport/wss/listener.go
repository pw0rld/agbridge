package wss

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
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
// All non-WSS paths return 404. For routes alongside WSS (e.g. /v1/enroll)
// use ListenWithHandler.
func Listen(ctx context.Context, addr string, tlsCfg *tls.Config) (*Listener, error) {
	return ListenWithHandler(ctx, addr, tlsCfg, nil)
}

// ListenWithHandler is like Listen but mounts extra as an HTTP handler
// for non-WSS requests on the same TLS port. WSS upgrade is preferred:
// any request with the WebSocket Upgrade header is routed to the WS
// handler regardless of path; everything else falls through to extra
// (which receives a 404 for unknown paths).
func ListenWithHandler(ctx context.Context, addr string, tlsCfg *tls.Config, extra http.Handler) (*Listener, error) {
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
	wsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			wsHandler.ServeHTTP(w, r)
			return
		}
		if extra != nil {
			extra.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	l.httpSrv = &http.Server{Handler: root}
	go l.httpSrv.Serve(ln)
	return l, nil
}

func isWebSocketUpgrade(r *http.Request) bool {
	conn := r.Header.Get("Connection")
	upg := r.Header.Get("Upgrade")
	return strings.Contains(strings.ToLower(conn), "upgrade") &&
		strings.EqualFold(upg, "websocket")
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
