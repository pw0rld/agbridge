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
