package wss

import (
	"context"
	"net/url"
	"sync"
	"testing"

	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
)

func TestDialerEndToEnd(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx := context.Background()
	ln, err := Listen(ctx, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var wg sync.WaitGroup
	wg.Add(1)
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
		_ = c.Send(ctx, proto.Frame{Type: proto.FrameTypePong, ReqID: f.ReqID})
	}()

	u := url.URL{Scheme: "wss", Host: ln.Addr().String(), Path: "/"}
	c, err := Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Send(ctx, proto.Frame{Type: proto.FrameTypePing, ReqID: "r3"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	f, err := c.Recv(ctx)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if f.Type != proto.FrameTypePong || f.ReqID != "r3" {
		t.Errorf("got %+v", f)
	}
	wg.Wait()
}
