package proto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestFrameTypeValues(t *testing.T) {
	tests := []struct {
		ft   FrameType
		want uint8
	}{
		{FrameTypePing, 1},
		{FrameTypePong, 2},
		{FrameTypeError, 3},
	}
	for _, tt := range tests {
		if uint8(tt.ft) != tt.want {
			t.Errorf("FrameType %v = %d, want %d", tt.ft, uint8(tt.ft), tt.want)
		}
	}
}

func TestFrameEncodeBasic(t *testing.T) {
	f := Frame{Type: FrameTypePing, ReqID: "abc", Payload: []byte("hello")}
	got, err := f.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	want := []byte{
		1,             // version
		1,             // type = Ping
		0, 3,          // reqid_len = 3
		'a', 'b', 'c', // reqid
		0, 0, 0, 5, // payload_len = 5
		'h', 'e', 'l', 'l', 'o',
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Encode: got % x, want % x", got, want)
	}
}

func TestFrameEncodeReqIDTooLong(t *testing.T) {
	f := Frame{Type: FrameTypePing, ReqID: strings.Repeat("x", 1<<16+1)}
	_, err := f.Encode()
	if !errors.Is(err, ErrReqIDTooLong) {
		t.Errorf("got %v, want ErrReqIDTooLong", err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	orig := Frame{Type: FrameTypePong, ReqID: "xyz", Payload: []byte("world")}
	encoded, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Type != orig.Type || got.ReqID != orig.ReqID || !bytes.Equal(got.Payload, orig.Payload) {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, orig)
	}
}

func TestFrameDecodeVersionMismatch(t *testing.T) {
	bad := []byte{99, 1, 0, 0, 0, 0, 0, 0}
	_, err := Decode(bad)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("got %v, want ErrVersionMismatch", err)
	}
}

func TestFrameTypePhase2Values(t *testing.T) {
	cases := []struct {
		ft   FrameType
		want uint8
	}{
		{FrameTypeHello, 10},
		{FrameTypeHelloAck, 11},
		{FrameTypeRoute, 12},
	}
	for _, c := range cases {
		if uint8(c.ft) != c.want {
			t.Errorf("FrameType %v = %d, want %d", c.ft, uint8(c.ft), c.want)
		}
	}
}

func TestFrameTypePhase3Values(t *testing.T) {
	cases := []struct {
		ft   FrameType
		want uint8
	}{
		{FrameTypeExecRequest, 20},
		{FrameTypeExecChunk, 21},
		{FrameTypeExecComplete, 22},
	}
	for _, c := range cases {
		if uint8(c.ft) != c.want {
			t.Errorf("FrameType %v = %d, want %d", c.ft, uint8(c.ft), c.want)
		}
	}
}

func TestFrameDecodeShort(t *testing.T) {
	cases := [][]byte{
		{},           // empty
		{1},          // only version
		{1, 1},       // missing reqid_len
		{1, 1, 0, 3}, // reqid_len=3 but no bytes
	}
	for i, c := range cases {
		if _, err := Decode(c); !errors.Is(err, ErrShortFrame) {
			t.Errorf("case %d: got %v, want ErrShortFrame", i, err)
		}
	}
}
