package e2e

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateStaticDifferent(t *testing.T) {
	a, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Pub(), b.Pub()) {
		t.Fatal("two GenerateStatic returned identical pubs")
	}
	if got := len(a.Pub()); got != 32 {
		t.Fatalf("expected 32B pub, got %d", got)
	}
	if got := len(a.RawPriv()); got != 32 {
		t.Fatalf("expected 32B priv, got %d", got)
	}
}

func TestPubBase64RoundTrip(t *testing.T) {
	k, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	s := k.PubBase64()
	dec, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(dec, k.Pub()) {
		t.Fatal("PubBase64 round-trip mismatch")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	orig, err := GenerateStatic()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "test.key")
	if err := orig.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadStatic(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig.Pub(), loaded.Pub()) {
		t.Fatal("pub mismatch after save/load")
	}
	if !bytes.Equal(orig.RawPriv(), loaded.RawPriv()) {
		t.Fatal("priv mismatch after save/load")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected 0600 perms, got %o", mode)
	}
}

func TestLoadStaticRejectsWrongSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(path, []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStatic(path); err == nil {
		t.Fatal("expected error for non-32-byte key file")
	}
}

func TestPubFromBase64(t *testing.T) {
	k, _ := GenerateStatic()
	parsed, err := PubFromBase64(k.PubBase64())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed, k.Pub()) {
		t.Fatal("PubFromBase64 mismatch")
	}
	if _, err := PubFromBase64("not-base64!!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
	if _, err := PubFromBase64(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("expected error for wrong-length key")
	}
}
