package wss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
)

// pair returns two Conns connected over an in-process TLS WebSocket. The
// returned cleanup closes both and shuts down the test server.
func pair(t *testing.T, opts KeepaliveOpts) (client, server *Conn, cleanup func()) {
	t.Helper()
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	upgrader := websocket.Upgrader{}
	got := make(chan *websocket.Conn, 1)
	httpSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		got <- ws
		// don't close ws here — test code owns it via server *Conn
		<-r.Context().Done()
	}))
	httpSrv.TLS = srvCfg
	httpSrv.StartTLS()

	d := websocket.Dialer{TLSClientConfig: cliCfg}
	u, _ := url.Parse(httpSrv.URL)
	u.Scheme = "wss"
	clientWS, _, err := d.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	serverWS := <-got
	client = NewConnWithKeepalive(clientWS, transport.Identity{Role: "client"}, opts)
	server = NewConnWithKeepalive(serverWS, transport.Identity{Role: "server"}, opts)
	return client, server, func() {
		_ = client.Close()
		_ = server.Close()
		httpSrv.Close()
	}
}

// driveRecv consumes Recv on c in a goroutine so gorilla's read pump runs
// (which is what surfaces incoming pings → auto-pong → SetPongHandler).
// Returns a stop channel so the caller can signal cleanup.
func driveRecv(c *Conn) chan struct{} {
	stop := make(chan struct{})
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-stop; cancel() }()
		for {
			if _, err := c.Recv(ctx); err != nil {
				return
			}
		}
	}()
	return stop
}

func TestKeepalivePongsKeepConnAlive(t *testing.T) {
	client, server, cleanup := pair(t, KeepaliveOpts{
		PingInterval: 30 * time.Millisecond,
		PongTimeout:  500 * time.Millisecond,
	})
	defer cleanup()
	stopC := driveRecv(client)
	stopS := driveRecv(server)
	defer close(stopC)
	defer close(stopS)

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if client.lastPongTime().IsZero() || server.lastPongTime().IsZero() {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		break
	}
	if client.lastPongTime().IsZero() {
		t.Errorf("client never observed a pong")
	}
	if server.lastPongTime().IsZero() {
		t.Errorf("server never observed a pong")
	}
	// Both conns should still be usable for app traffic.
	if err := client.Send(context.Background(), proto.Frame{Type: proto.FrameTypePing, ReqID: "ok"}); err != nil {
		t.Errorf("client.Send after pongs: %v", err)
	}
}

func TestKeepaliveClosesOnPongTimeout(t *testing.T) {
	client, _, cleanup := pair(t, KeepaliveOpts{
		PingInterval: 30 * time.Millisecond,
		PongTimeout:  150 * time.Millisecond,
	})
	defer cleanup()
	// Intentionally don't drive Recv on either side. The peer never
	// auto-pongs because its read pump isn't running, so client's keepalive
	// goroutine should hit PongTimeout and close the conn.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := client.Recv(ctx)
		cancel()
		if err != nil && err != context.DeadlineExceeded {
			return // got the network error we expected
		}
	}
	t.Fatalf("client conn never closed despite peer not ponging")
}

func TestKeepaliveDisable(t *testing.T) {
	client, _, cleanup := pair(t, KeepaliveOpts{Disable: true})
	defer cleanup()
	time.Sleep(50 * time.Millisecond)
	if client.lastPongTime() != (time.Time{}) {
		t.Errorf("Disable=true but lastPong was set: %v", client.lastPongTime())
	}
}
