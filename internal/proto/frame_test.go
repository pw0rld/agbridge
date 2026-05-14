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
