package wss

import (
	"context"
	"net/url"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
)

func TestListenerAcceptsAndReceives(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := Listen(ctx, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	var got proto.Frame
	go func() {
		defer wg.Done()
		c, err := ln.Accept(ctx)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer c.Close()
		f, err := c.Recv(ctx)
		if err != nil {
			t.Errorf("recv: %v", err)
			return
		}
		got = f
	}()

	dialer := websocket.Dialer{TLSClientConfig: cliCfg}
	u := url.URL{Scheme: "wss", Host: ln.Addr().String(), Path: "/"}
	ws, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	b, _ := proto.Frame{Type: proto.FrameTypePing, ReqID: "r2"}.Encode()
	if err := ws.WriteMessage(websocket.BinaryMessage, b); err != nil {
		t.Fatalf("write: %v", err)
	}
	wg.Wait()
	if got.Type != proto.FrameTypePing || got.ReqID != "r2" {
		t.Errorf("got %+v", got)
	}
}
