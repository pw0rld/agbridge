package handshake

import (
	"errors"
	"testing"
)

func TestHelloRoundTrip(t *testing.T) {
	h := Hello{Role: "bridge", Name: "claude-laptop", Secret: "my-api-key", TargetDaemon: "lab01"}
	b, err := h.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeHello(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Role != h.Role || got.Name != h.Name || got.Secret != h.Secret || got.TargetDaemon != h.TargetDaemon {
		t.Errorf("got %+v, want %+v", got, h)
	}
}

// TestHelloWireBackwardCompat pins the backward-compatible extension:
// generation-1 wire bytes (no trailing version byte) must keep decoding
// with EffectiveVersion()==1, and a single version byte round-trips.
func TestHelloWireBackwardCompat(t *testing.T) {
	h := Hello{Role: "daemon", Name: "lab01", Secret: "tok"}
	b, err := h.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Current encoding always appends the version byte.
	got, err := DecodeHello(b)
	if err != nil {
		t.Fatalf("decode full: %v", err)
	}
	if got.EffectiveVersion() != ProtocolVersion {
		t.Fatalf("version = %d, want %d", got.EffectiveVersion(), ProtocolVersion)
	}
	// Generation-1 wire: strip the version byte.
	got, err = DecodeHello(b[:len(b)-1])
	if err != nil {
		t.Fatalf("decode legacy wire: %v", err)
	}
	if got.EffectiveVersion() != 1 {
		t.Fatalf("legacy EffectiveVersion = %d, want 1", got.EffectiveVersion())
	}
	// Trailing garbage beyond one byte is malformed.
	if _, err := DecodeHello(append(b, 0x00)); err == nil {
		t.Fatal("expected malformed for oversized trailing bytes")
	}
}

func TestHelloDecodeShort(t *testing.T) {
	if _, err := DecodeHello([]byte{}); !errors.Is(err, ErrHelloMalformed) {
		t.Errorf("got %v, want ErrHelloMalformed", err)
	}
}

func TestHelloDaemonOmitsTarget(t *testing.T) {
	h := Hello{Role: "daemon", Name: "lab01", Secret: "tok"}
	b, err := h.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeHello(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TargetDaemon != "" {
		t.Errorf("daemon TargetDaemon should be empty, got %q", got.TargetDaemon)
	}
}
