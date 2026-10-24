//go:build unix

package gateway

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Two TokenStore instances on one path model sibling processes (CLI
// issue-token vs running gateway). Concurrent Consume of the same
// one-shot token must have exactly one winner — the flock around
// reload→mark→flush closes the window where both reload "unused" and
// both succeed.
func TestConcurrentConsumeAcrossStoresSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	a, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.Issue(TokenRequest{Role: "daemon", Name: "d1", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}

	var ok int64
	var wg sync.WaitGroup
	for _, s := range []*TokenStore{a, b} {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(s *TokenStore) {
				defer wg.Done()
				if _, err := s.Consume(tok.Token); err == nil {
					atomic.AddInt64(&ok, 1)
				}
			}(s)
		}
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("concurrent consume successes = %d, want exactly 1 (double-spend)", ok)
	}
}

// Concurrent Issue from two stores must not lose writes: each flush
// rewrites the whole map, so without cross-process locking a store
// could write back a snapshot predating the sibling's issues.
func TestConcurrentIssueAcrossStoresNoLostWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	a, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, s := range []*TokenStore{a, b} {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(s *TokenStore, i int) {
				defer wg.Done()
				if _, err := s.Issue(TokenRequest{Role: "daemon", Name: "d", TTL: time.Minute}); err != nil {
					t.Errorf("issue: %v", err)
				}
			}(s, i)
		}
	}
	wg.Wait()

	reopened, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.List()); got != 16 {
		t.Fatalf("persisted tokens = %d, want 16 (lost updates)", got)
	}
}
