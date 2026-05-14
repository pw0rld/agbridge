package fileproto

import (
	"bytes"
	"testing"
)

func TestFileReadRequestRoundTrip(t *testing.T) {
	orig := FileReadRequest{Path: "/tmp/x", MaxSize: 1024}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeFileReadRequest(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestFileWriteRequestRoundTrip(t *testing.T) {
	orig := FileWriteRequest{Path: "/tmp/x", Mode: 0o600}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeFileWriteRequest(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestFileChunkRoundTrip(t *testing.T) {
	orig := FileChunk{Data: []byte("\x00\x01\xff hello"), Eof: true}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeFileChunk(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got.Data, orig.Data) || got.Eof != orig.Eof {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestFileCompleteRoundTrip(t *testing.T) {
	orig := FileComplete{Size: 1234, Sha256: "abc", Err: ""}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeFileComplete(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestFileCompleteErrRoundTrip(t *testing.T) {
	orig := FileComplete{Err: "path_forbidden"}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeFileComplete(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != orig {
		t.Errorf("round trip: got %+v, want %+v", got, orig)
	}
}

func TestDecodeBadJSON(t *testing.T) {
	if _, err := DecodeFileReadRequest([]byte("{")); err == nil {
		t.Error("expected error on bad JSON")
	}
	if _, err := DecodeFileWriteRequest([]byte("{")); err == nil {
		t.Error("expected error on bad JSON")
	}
	if _, err := DecodeFileChunk([]byte("{")); err == nil {
		t.Error("expected error on bad JSON")
	}
	if _, err := DecodeFileComplete([]byte("{")); err == nil {
		t.Error("expected error on bad JSON")
	}
}
