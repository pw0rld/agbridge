package wss

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pw0rld/agbridge/internal/transport/testcerts"
)

// TestListenWithHandlerSharedTLS verifies that the same TLS port can
// serve both WSS upgrades AND custom HTTP routes.
func TestListenWithHandlerSharedTLS(t *testing.T) {
	serverTLS, clientTLS := testcerts.MustGenerate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	httpHandler := http.NewServeMux()
	httpHandler.HandleFunc("/v1/test", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, r.Body)
	})

	ln, err := ListenWithHandler(ctx, "127.0.0.1:0", serverTLS, httpHandler)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().String()

	// 1. HTTP POST to /v1/test echoes body.
	cl := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
		Timeout:   3 * time.Second,
	}
	resp, err := cl.Post("https://"+addr+"/v1/test", "text/plain", bytes.NewReader([]byte("ping")))
	if err != nil {
		t.Fatalf("http post: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ping" {
		t.Fatalf("echo mismatch: %q", body)
	}

	// 2. Concurrently dial WSS and accept on listener side.
	dialer := &websocket.Dialer{TLSClientConfig: clientTLS, HandshakeTimeout: 3 * time.Second}
	doneAccept := make(chan struct{})
	go func() {
		defer close(doneAccept)
		c, err := ln.Accept(ctx)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		_ = c.Close()
	}()
	ws, _, err := dialer.Dial("wss://"+addr+"/", nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	_ = ws.Close()
	select {
	case <-doneAccept:
	case <-time.After(3 * time.Second):
		t.Fatal("Accept never fired on WSS connect")
	}
}

func TestListenStillReturns404WhenNoExtraHandler(t *testing.T) {
	serverTLS, clientTLS := testcerts.MustGenerate(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := Listen(ctx, "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	cl := &http.Client{
		Transport: &http.Transport{TLSClientConfig: clientTLS},
		Timeout:   3 * time.Second,
	}
	resp, err := cl.Get("https://" + ln.Addr().String() + "/v1/unknown")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}
