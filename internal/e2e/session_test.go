package e2e

import (
	"bytes"
	"testing"
)

// completeHandshake is a test helper that runs a successful IK handshake
// between fresh bridge and daemon keys, returning both Sessions.
func completeHandshake(t *testing.T) (initSess, respSess *Session) {
	t.Helper()
	bridge, _ := GenerateStatic()
	daemon, _ := GenerateStatic()
	prologue := []byte("ag/v1|test|lab01")
	init, _ := NewInitiator(bridge, daemon.Pub(), prologue)
	msg1, _ := init.WriteMessage1()
	resp, _ := NewResponder(daemon, prologue)
	if _, err := resp.ReadMessage1(msg1); err != nil {
		t.Fatal(err)
	}
	msg2, ds, err := resp.WriteMessage2()
	if err != nil {
		t.Fatal(err)
	}
	is, err := init.ReadMessage2(msg2)
	if err != nil {
		t.Fatal(err)
	}
	return is, ds
}

func TestSessionEncryptDecryptRoundTrip(t *testing.T) {
	bridgeSess, daemonSess := completeHandshake(t)

	ad := []byte("session-id|reqid")
	plain := []byte("hello daemon, this is bridge")

	ct, err := bridgeSess.Encrypt(ad, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ct, plain) {
		t.Fatal("ciphertext equals plaintext — encryption is a no-op")
	}
	got, err := daemonSess.Decrypt(ad, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plain)
	}
}

func TestSessionReverseDirection(t *testing.T) {
	bridgeSess, daemonSess := completeHandshake(t)

	ad := []byte("ad")
	plain := []byte("daemon → bridge reply chunk")

	ct, err := daemonSess.Encrypt(ad, plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := bridgeSess.Decrypt(ad, ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("reverse round-trip mismatch: got %q, want %q", got, plain)
	}
}

func TestSessionADBindingMismatch(t *testing.T) {
	bridgeSess, daemonSess := completeHandshake(t)
	ad1 := []byte("ad-original")
	ad2 := []byte("ad-tampered")
	plain := []byte("payload")

	ct, _ := bridgeSess.Encrypt(ad1, plain)
	if _, err := daemonSess.Decrypt(ad2, ct); err == nil {
		t.Fatal("expected decrypt to fail when AD differs")
	}
}

func TestSessionCiphertextTamperingFails(t *testing.T) {
	bridgeSess, daemonSess := completeHandshake(t)
	ad := []byte("ad")
	plain := []byte("payload")

	ct, _ := bridgeSess.Encrypt(ad, plain)
	// Flip one byte in the ciphertext (not in the tag) — Poly1305 must catch it.
	if len(ct) < 5 {
		t.Fatal("ciphertext shorter than expected")
	}
	ct[2] ^= 0xff
	if _, err := daemonSess.Decrypt(ad, ct); err == nil {
		t.Fatal("expected decrypt to fail on ciphertext tampering")
	}
}

func TestSessionNonceCountersAdvance(t *testing.T) {
	bridgeSess, daemonSess := completeHandshake(t)
	ad := []byte("ad")

	// Send three messages; each must decrypt with the correct sequential nonce.
	for i, msg := range [][]byte{[]byte("one"), []byte("two"), []byte("three")} {
		ct, err := bridgeSess.Encrypt(ad, msg)
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}
		got, err := daemonSess.Decrypt(ad, ct)
		if err != nil {
			t.Fatalf("decrypt %d: %v", i, err)
		}
		if !bytes.Equal(got, msg) {
			t.Fatalf("msg %d mismatch: got %q want %q", i, got, msg)
		}
	}
}
