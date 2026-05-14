package auth

import (
	"bytes"
	"errors"
	"testing"
)

func TestSignAndVerify(t *testing.T) {
	key := []byte("super-secret-key")
	frame := []byte("encoded-frame-bytes")
	signed := SignFrame(key, frame)
	if !bytes.HasPrefix(signed, frame) {
		t.Fatalf("signed prefix mismatch")
	}
	if len(signed) != len(frame)+HMACSize {
		t.Fatalf("got %d bytes, want %d", len(signed), len(frame)+HMACSize)
	}
	got, err := VerifyFrame(key, signed)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !bytes.Equal(got, frame) {
		t.Fatalf("payload mismatch: %q vs %q", got, frame)
	}
}

func TestVerifyTampered(t *testing.T) {
	key := []byte("k")
	signed := SignFrame(key, []byte("hello"))
	signed[0] = 'X' // flip first byte
	_, err := VerifyFrame(key, signed)
	if !errors.Is(err, ErrBadMAC) {
		t.Errorf("got %v, want ErrBadMAC", err)
	}
}

func TestVerifyShort(t *testing.T) {
	if _, err := VerifyFrame([]byte("k"), []byte("tooshort")); !errors.Is(err, ErrShortMAC) {
		t.Errorf("got %v, want ErrShortMAC", err)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	signed := SignFrame([]byte("k1"), []byte("payload"))
	if _, err := VerifyFrame([]byte("k2"), signed); !errors.Is(err, ErrBadMAC) {
		t.Errorf("got %v, want ErrBadMAC", err)
	}
}
