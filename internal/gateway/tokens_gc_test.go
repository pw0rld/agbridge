package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenStoreGCLoopSweeps(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTokenStore(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.Issue(TokenRequest{Role: "daemon", Name: "d1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Issue(TokenRequest{Role: "daemon", Name: "d2", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(tok.Token); err != nil {
		t.Fatal(err)
	}
	// Age the consumption stamp beyond the retention window.
	store.mu.Lock()
	store.toks[tok.Token].UsedAt = time.Now().Add(-2 * time.Hour)
	if err := store.flushLocked(); err != nil {
		t.Fatal(err)
	}
	store.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		store.GCLoop(ctx, 10*time.Millisecond, time.Hour, func(error) {
			t.Error("token GC sweep failed")
		})
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		live := store.List()
		if len(live) == 1 && live[0].Token == fresh.Token {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GCLoop left %+v, want only the fresh unused token", live)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GCLoop ignores ctx cancellation")
	}
}
