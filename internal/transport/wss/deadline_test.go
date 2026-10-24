package wss

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
)

// Recv must unblock promptly when ctx is cancelled, even though gorilla's
// ReadMessage ignores contexts (the bridge closes the conn via
// context.AfterFunc).
func TestRecvUnblocksOnCtxCancel(t *testing.T) {
	client, _, cleanup := pair(t, KeepaliveOpts{Disable: true})
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	done := make(chan error, 1)
	go func() {
		_, err := client.Recv(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Recv returned nil error after ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv still blocked >2s after ctx cancel")
	}
}

// With keepalive pings but a peer that never reads (so no pongs come
// back), the Recv idle deadline must fail the read on its own — set well
// before PongTimeout so it is the mechanism under test.
func TestRecvIdleDeadlineFires(t *testing.T) {
	client, _, cleanup := pair(t, KeepaliveOpts{
		PingInterval:    30 * time.Millisecond,
		PongTimeout:     10 * time.Second, // keepalive must NOT be the killer
		ReadIdleTimeout: 150 * time.Millisecond,
	})
	defer cleanup()

	ctx := context.Background()
	start := time.Now()
	_, err := client.Recv(ctx)
	if err == nil {
		t.Fatal("Recv returned nil error on idle deadline")
	}
	if el := time.Since(start); el < 100*time.Millisecond || el > 3*time.Second {
		t.Errorf("Recv idle timeout after %v, want ~150ms", el)
	}
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "deadline") {
		t.Logf("idle timeout error (accepted): %v", err)
	}
}

// Pong arrivals refresh the idle deadline, so an idle-but-live conn
// survives far longer than ReadIdleTimeout.
func TestPongsRefreshIdleDeadline(t *testing.T) {
	client, server, cleanup := pair(t, KeepaliveOpts{
		PingInterval:    30 * time.Millisecond,
		PongTimeout:     5 * time.Second,
		ReadIdleTimeout: 150 * time.Millisecond,
	})
	defer cleanup()
	stopS := driveRecv(server) // server reads → auto-pongs keep client warm
	defer close(stopS)
	// Client needs its read pump too; drive it and swallow frames.
	stopC := driveRecv(client)
	defer close(stopC)

	// If the idle deadline were never refreshed, Recv would die at ~150ms.
	// Instead the pair must still be healthy at 600ms.
	time.Sleep(600 * time.Millisecond)
	if err := client.Send(context.Background(), proto.Frame{Type: proto.FrameTypePing, ReqID: "alive"}); err != nil {
		t.Fatalf("client conn died despite pong refresh: %v", err)
	}
}

// SetReadLimit must refuse oversized inbound messages, closing the conn
// with an error rather than buffering unbounded data.
func TestReadLimitRefusesOversized(t *testing.T) {
	client, server, cleanup := pair(t, KeepaliveOpts{Disable: true})
	defer cleanup()

	// defaultReadLimit (32 MiB) is impractical to exceed in a unit test;
	// lower the client's limit the same way production set it.
	client.ws.SetReadLimit(1024)

	big := proto.Frame{Type: proto.FrameTypeError, ReqID: "big", Payload: make([]byte, 4096)}
	if err := server.Send(context.Background(), big); err != nil {
		t.Fatalf("server.Send: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.Recv(ctx); err == nil {
		t.Fatal("Recv accepted message over read limit")
	}
}
