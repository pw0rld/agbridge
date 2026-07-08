package gateway

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssueBridgeMultiTarget(t *testing.T) {
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.Issue(TokenRequest{Role: "bridge", Name: "laptop", Targets: []string{"lab01", "lab02"}})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, err := store.Consume(tok.Token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(got.Targets) != 2 || got.Targets[0] != "lab01" || got.Targets[1] != "lab02" {
		t.Fatalf("targets lost: %v", got.Targets)
	}
	if got.Target != "lab01" {
		t.Fatalf("Target compat field = %q, want lab01", got.Target)
	}
}

func TestIssueBridgeSingleTargetNormalizes(t *testing.T) {
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.Issue(TokenRequest{Role: "bridge", Name: "laptop", Target: "lab01"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	got, _ := store.Consume(tok.Token)
	if len(got.Targets) != 1 || got.Targets[0] != "lab01" {
		t.Fatalf("single target not normalized into Targets: %v", got.Targets)
	}
}

func TestIssueBridgeNoTargetFails(t *testing.T) {
	store, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(TokenRequest{Role: "bridge", Name: "laptop"}); err == nil {
		t.Fatal("expected error when bridge has no target")
	}
}

func TestTokenIssueConsume(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTokenStore(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.Issue(TokenRequest{
		Role:   "bridge",
		Name:   "test-laptop",
		Target: "lab01",
		TTL:    15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token, "et_") {
		t.Fatalf("token should start with et_, got %q", tok.Token)
	}
	consumed, err := store.Consume(tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Name != "test-laptop" {
		t.Fatalf("name mismatch: %q", consumed.Name)
	}
	// Second consume must fail (one-shot).
	if _, err := store.Consume(tok.Token); err == nil {
		t.Fatal("expected error on second consume")
	}
}

func TestTokenExpiry(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewTokenStore(filepath.Join(dir, "tokens.json"))
	tok, _ := store.Issue(TokenRequest{Role: "daemon", Name: "lab", TTL: time.Millisecond})
	time.Sleep(5 * time.Millisecond)
	if _, err := store.Consume(tok.Token); err == nil {
		t.Fatal("expected expiry error")
	}
}

func TestTokenPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	s1, _ := NewTokenStore(path)
	tok, _ := s1.Issue(TokenRequest{Role: "daemon", Name: "lab", TTL: time.Hour})

	s2, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Consume(tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lab" {
		t.Fatalf("persisted token corrupt: %+v", got)
	}
}

func TestTokenDaemonPolicy(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewTokenStore(filepath.Join(dir, "tokens.json"))
	policy := &DaemonPolicy{
		AllowedPaths:   []string{"/home/me/projects"},
		ForbiddenPorts: []int{22},
	}
	tok, _ := store.Issue(TokenRequest{Role: "daemon", Name: "lab", TTL: time.Hour, Policy: policy})
	got, _ := store.Consume(tok.Token)
	if got.Policy == nil || len(got.Policy.AllowedPaths) != 1 {
		t.Fatalf("policy lost: %+v", got.Policy)
	}
}

func TestTokenIssueRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewTokenStore(filepath.Join(dir, "tokens.json"))
	if _, err := store.Issue(TokenRequest{Role: "alien", Name: "x"}); err == nil {
		t.Fatal("expected reject for unknown role")
	}
	if _, err := store.Issue(TokenRequest{Role: "bridge", Name: ""}); err == nil {
		t.Fatal("expected reject for empty name")
	}
	if _, err := store.Issue(TokenRequest{Role: "bridge", Name: "x"}); err == nil {
		t.Fatal("expected reject: bridge without target")
	}
}

func TestTokenList(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewTokenStore(filepath.Join(dir, "tokens.json"))
	_, _ = store.Issue(TokenRequest{Role: "daemon", Name: "lab1", TTL: time.Hour})
	_, _ = store.Issue(TokenRequest{Role: "daemon", Name: "lab2", TTL: time.Hour})
	got := store.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(got))
	}
}
