package cliclient

import (
	"context"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
)

// TestUnregisterWakesBlockedReader pins the fix for abandoned
// transfers on a pooled connection: runReader hands frames to a call's
// channel, and a handler that returned mid-stream (Ctrl-C during a
// download, request timeout) must not leave the shared reader blocked
// on a full channel — that would wedge every later request on the same
// connection until the pool entry is garbage collected.
func TestUnregisterWakesBlockedReader(t *testing.T) {
	r := &Router{
		ctx:     context.Background(),
		pending: make(map[string]*pendingCall),
	}
	_ = r.registerCall("req1")
	r.mu.Lock()
	pc0 := r.pending["req1"]
	r.mu.Unlock()

	// Fill the buffer so the reader's send would block.
	for i := 0; i < cap(pc0.ch); i++ {
		pc0.ch <- proto.Frame{}
	}

	woke := make(chan struct{})
	go func() {
		r.mu.Lock()
		pc, ok := r.pending["req1"]
		r.mu.Unlock()
		if !ok {
			close(woke)
			return
		}
		select {
		case pc.ch <- proto.Frame{}: // would block: buffer is full
		case <-pc.done:
			close(woke)
		}
	}()

	// Give the goroutine time to reach the blocking select.
	time.Sleep(50 * time.Millisecond)
	select {
	case <-woke:
		t.Fatal("test setup: send did not block on the full channel")
	default:
	}

	r.unregisterCall("req1")

	select {
	case <-woke:
	case <-time.After(2 * time.Second):
		t.Fatal("unregisterCall did not wake a reader blocked on a full call channel")
	}
}
