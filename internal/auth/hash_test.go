package auth

import (
	"strings"
	"testing"
)

func TestSHA256Hex(t *testing.T) {
	got := SHA256Hex([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSecretMatches(t *testing.T) {
	hash := SHA256Hex([]byte("supersecret"))
	if !SecretMatches("supersecret", "sha256:"+hash) {
		t.Errorf("matching secret returned false")
	}
	if SecretMatches("wrong", "sha256:"+hash) {
		t.Errorf("non-matching secret returned true")
	}
	if SecretMatches("supersecret", "md5:"+hash) {
		t.Errorf("non-sha256 prefix should be rejected")
	}
	if !strings.HasPrefix("sha256:"+hash, "sha256:") {
		t.Fatal("internal sanity")
	}
}
