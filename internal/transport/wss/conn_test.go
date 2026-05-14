package wss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
)

func TestConnSendRecv(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	upgrader := websocket.Upgrader{}
	got := make(chan proto.Frame, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer ws.Close()
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Errorf("read: %v", err)
			return
		}
		f, err := proto.Decode(data)
		if err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		got <- f
	}))
	srv.TLS = srvCfg
	srv.StartTLS()
	defer srv.Close()

	dialer := websocket.Dialer{TLSClientConfig: cliCfg}
	u, _ := url.Parse(srv.URL)
	u.Scheme = "wss"
	ws, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := NewConn(ws, transport.Identity{Role: "test"})
	if err := c.Send(context.Background(), proto.Frame{Type: proto.FrameTypePing, ReqID: "r1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	f := <-got
	if f.Type != proto.FrameTypePing || f.ReqID != "r1" {
		t.Errorf("got %+v", f)
	}
}
