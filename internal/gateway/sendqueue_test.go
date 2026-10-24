package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
)

func TestFrameQueueDeliversInOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 8)
	q := newFrameQueue(ctx, func(_ context.Context, f proto.Frame) error {
		got <- f.ReqID
		return nil
	})
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		if err := q.submit(proto.Frame{ReqID: id}); err != nil {
			t.Fatalf("submit %s: %v", id, err)
		}
	}
	for _, want := range []string{"1", "2", "3", "4", "5"} {
		select {
		case id := <-got:
			if id != want {
				t.Fatalf("order: got %s, want %s", id, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for frame %s", want)
		}
	}
}

// A stalled sender must not turn submit into a blocking call: submits
// succeed up to the bound and then fail fast with ErrSendQueueFull
// (the caller severs the session instead of back-pressuring the
// daemon read loop).
func TestFrameQueueOverflowFailsFast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newFrameQueue(ctx, func(ctx context.Context, _ proto.Frame) error {
		<-ctx.Done() // sender blocks on the first frame until cancel
		return ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		var err error
		for i := 0; i < bridgeQueueDepth+8; i++ {
			if err = q.submit(proto.Frame{Payload: []byte("x")}); err != nil {
				break
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("overflow never happened")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submit blocked on a full queue")
	}
}

// The byte bound must trip even while the frame-count bound is far
// from capacity (frames can legitimately be megabytes each).
func TestFrameQueueByteBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := newFrameQueue(ctx, func(ctx context.Context, _ proto.Frame) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err := q.submit(proto.Frame{Payload: make([]byte, bridgeQueueMaxBytes)}); err != nil {
		t.Fatalf("first big frame: %v", err)
	}
	// One frame is already parked in the sender; the queue itself holds
	// zero frames but the accounting still covers max bytes.
	if err := q.submit(proto.Frame{Payload: make([]byte, 1)}); err == nil {
		t.Fatal("expected overflow past the byte bound")
	}
}

func TestFrameQueueSubmitAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := newFrameQueue(ctx, func(_ context.Context, _ proto.Frame) error { return nil })
	cancel()
	// Give the sender a moment to exit; submit must reject regardless.
	time.Sleep(20 * time.Millisecond)
	if err := q.submit(proto.Frame{}); err == nil {
		t.Fatal("submit after cancel must fail")
	}
}

// The property this whole queue exists for: a bridge whose conn never
// drains must not head-of-line block the daemon's read loop or the
// other subscribers of that daemon. Deliver must keep returning and
// the healthy subscriber must keep receiving, while the stalled
// subscription gets pruned once its queue overflows.
func TestDeliverDoesNotBlockBehindSlowBridge(t *testing.T) {
	p := NewDaemonProxy(&stubConn{}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slow := newFrameQueue(ctx, func(ctx context.Context, _ proto.Frame) error {
		<-ctx.Done() // this bridge's conn never drains
		return ctx.Err()
	})
	p.Subscribe("slow", func(_ context.Context, f proto.Frame) error { return slow.submit(f) })

	fast := make(chan struct{}, bridgeQueueDepth+16)
	p.Subscribe("fast", func(context.Context, proto.Frame) error { fast <- struct{}{}; return nil })

	total := bridgeQueueDepth + 8
	done := make(chan int, 1)
	go func() {
		n := 0
		for i := 0; i < total; i++ {
			_ = p.Deliver(ctx, proto.Frame{Payload: make([]byte, 64)})
			n++
		}
		done <- n
	}()
	select {
	case n := <-done:
		if n != total {
			t.Fatalf("Deliver loop aborted: %d/%d", n, total)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deliver blocked behind the slow bridge")
	}

	deadline := time.After(2 * time.Second)
	for got := 0; got < total; got++ {
		select {
		case <-fast:
		case <-deadline:
			t.Fatalf("fast subscriber starved: got %d of %d frames", got, total)
		}
	}

	// The stalled subscription must have been pruned on overflow; the
	// healthy one remains.
	p.mu.Lock()
	subs := len(p.subs)
	p.mu.Unlock()
	if subs != 1 {
		var alive []string
		p.mu.Lock()
		for w := range p.subs {
			alive = append(alive, w.name)
		}
		p.mu.Unlock()
		t.Fatalf("live subscriptions = %d %v, want 1 [fast]", subs, alive)
	}
}
