package main

import (
	"context"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/config"
)

func newRouterForTest(t *testing.T) *router {
	t.Helper()
	r, err := newRouter(context.Background(), nil, []byte("k"), &config.BridgeConfig{E2EMode: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRouterReplaceConnClosesPending(t *testing.T) {
	r := newRouterForTest(t)
	ch1 := r.registerCall("req1")
	ch2 := r.registerCall("req2")
	r.replaceConn(nil)
	for name, ch := range map[string]<-chan any{"ch1": readable(ch1), "ch2": readable(ch2)} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("%s yielded a value, expected closed channel", name)
			}
		case <-time.After(time.Second):
			t.Errorf("%s: replaceConn did not close pending channel", name)
		}
	}
	r.mu.Lock()
	n := len(r.pending)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("pending map size = %d, want 0", n)
	}
}

func TestRouterReplaceConnSwapsActiveConn(t *testing.T) {
	r := newRouterForTest(t)
	if got := r.currentConn(); got != nil {
		t.Fatalf("expected nil initial conn, got %v", got)
	}
	r.replaceConn(nil)
	if got := r.currentConn(); got != nil {
		t.Fatalf("currentConn = %v, want nil", got)
	}
}

// readable adapts <-chan proto.Frame to <-chan any without import churn in
// the table above.
func readable[T any](ch <-chan T) <-chan any {
	out := make(chan any)
	go func() {
		defer close(out)
		for v := range ch {
			out <- v
		}
	}()
	return out
}
