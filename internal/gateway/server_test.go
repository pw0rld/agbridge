package gateway

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func TestGatewayEchoesPing(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, err := Run(ctx, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	u := url.URL{Scheme: "wss", Host: addr.String(), Path: "/"}
	c, err := wss.Dial(ctx, u.String(), transport.Credentials{}, cliCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if err := c.Send(ctx, proto.Frame{Type: proto.FrameTypePing, ReqID: "r4"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	deadline, _ := context.WithTimeout(ctx, 2*time.Second)
	f, err := c.Recv(deadline)
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if f.Type != proto.FrameTypePong || f.ReqID != "r4" {
		t.Errorf("got %+v", f)
	}
}
