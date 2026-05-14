package streamproto

import (
	"bytes"
	"testing"
)

func TestStreamOpenRoundTrip(t *testing.T) {
	orig := StreamOpen{StreamID: "abc", RemoteHost: "127.0.0.1", RemotePort: 5432}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeStreamOpen(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestStreamAckRoundTrip(t *testing.T) {
	cases := []StreamAck{
		{StreamID: "abc", Ok: true},
		{StreamID: "abc", Ok: false, Err: "port_forbidden"},
	}
	for _, orig := range cases {
		b, err := orig.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		got, err := DecodeStreamAck(b)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if got != orig {
			t.Errorf("round trip: got %+v, want %+v", got, orig)
		}
	}
}

func TestStreamDataRoundTrip(t *testing.T) {
	orig := StreamData{StreamID: "abc", Data: []byte("\x00\x01\xff hello")}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeStreamData(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.StreamID != orig.StreamID || !bytes.Equal(got.Data, orig.Data) {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestStreamCloseRoundTrip(t *testing.T) {
	orig := StreamClose{StreamID: "abc", Err: "EOF"}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeStreamClose(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestDecodeBadJSON(t *testing.T) {
	if _, err := DecodeStreamOpen([]byte("{")); err == nil {
		t.Error("expected error")
	}
	if _, err := DecodeStreamAck([]byte("{")); err == nil {
		t.Error("expected error")
	}
	if _, err := DecodeStreamData([]byte("{")); err == nil {
		t.Error("expected error")
	}
	if _, err := DecodeStreamClose([]byte("{")); err == nil {
		t.Error("expected error")
	}
}
