package main

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/gateway"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func TestSmokeGatewayBridgeDaemon(t *testing.T) {
	srvCfg, cliCfg := testcerts.MustGenerate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr, err := gateway.Run(ctx, "127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	endpoint := (&url.URL{Scheme: "wss", Host: addr.String(), Path: "/"}).String()

	dial := func(role, reqID string) error {
		c, err := wss.Dial(ctx, endpoint, transport.Credentials{}, cliCfg)
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Send(ctx, proto.Frame{Type: proto.FrameTypePing, ReqID: reqID}); err != nil {
			return err
		}
		deadline, _ := context.WithTimeout(ctx, 2*time.Second)
		f, err := c.Recv(deadline)
		if err != nil {
			return err
		}
		if f.Type != proto.FrameTypePong || f.ReqID != reqID {
			t.Errorf("%s got %+v", role, f)
		}
		return nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := dial("bridge", "bridge-1"); err != nil {
			t.Errorf("bridge: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := dial("daemon", "daemon-1"); err != nil {
			t.Errorf("daemon: %v", err)
		}
	}()
	wg.Wait()
}
