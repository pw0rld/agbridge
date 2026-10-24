package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIssueRefusesCorruptTokenFile pins the fail-closed reload: when
// tokens.json cannot be parsed, Issue must not fall back to the
// pre-lock snapshot and flush it over the file — that would drop a
// token a sibling process just issued.
func TestIssueRefusesCorruptTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(TokenRequest{Role: "daemon", Name: "d1", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}

	const corrupt = `{"et_broken": `
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(TokenRequest{Role: "daemon", Name: "d2", TTL: time.Hour}); err == nil {
		t.Fatal("Issue succeeded on a corrupt token file; it would overwrite it with a stale snapshot")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != corrupt {
		t.Fatalf("token file was rewritten despite the reload failure: %q", got)
	}
}

// TestConsumeRefusesCorruptTokenFile is the same guarantee on the
// consume path (a consumed token must never be resurrected by a flush
// of the stale map).
func TestConsumeRefusesCorruptTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := NewTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := store.Issue(TokenRequest{Role: "daemon", Name: "d1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	const corrupt = `not-json`
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Consume(tok.Token); err == nil {
		t.Fatal("Consume succeeded on a corrupt token file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != corrupt {
		t.Fatalf("token file was rewritten despite the reload failure: %q", got)
	}
}
