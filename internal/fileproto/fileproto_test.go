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

func TestChunkBinaryCodec(t *testing.T) {
	cases := []FileChunk{
		{Data: []byte("hello"), Eof: false},
		{Data: []byte("last"), Eof: true},
		{Data: nil, Eof: true}, // zero-length EOF sentinel
		{Data: []byte{}, Eof: false},
		{Data: []byte{0x00, 0xFF, 0x01}, Eof: true}, // raw bytes must not be escaped
	}
	for _, want := range cases {
		got, err := DecodeChunkBinary(EncodeChunkBinary(want))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Eof != want.Eof || string(got.Data) != string(want.Data) {
			t.Errorf("round trip: got %+v, want %+v", got, want)
		}
	}
	// Binary form must be strictly smaller than JSON+base64 for data.
	data := make([]byte, 1024)
	jsonEnc, _ := FileChunk{Data: data}.Encode()
	if bin := EncodeChunkBinary(FileChunk{Data: data}); len(bin) >= len(jsonEnc) {
		t.Errorf("binary encoding %d bytes not smaller than JSON %d bytes", len(bin), len(jsonEnc))
	}
	if _, err := DecodeChunkBinary(nil); err == nil {
		t.Error("expected error on empty binary payload")
	}
}

func TestRequestBinaryFieldDefaultFalse(t *testing.T) {
	// Old bridges send requests without the binary field: it must
	// decode as false so daemons keep the JSON chunk encoding.
	if rr, err := DecodeFileReadRequest([]byte(`{"path":"/x"}`)); err != nil || rr.Binary {
		t.Errorf("old read request must default to false: %+v, %v", rr, err)
	}
	rd, err := DecodeFileReadRequest([]byte(`{"path":"/x","binary":true}`))
	if err != nil || !rd.Binary {
		t.Errorf("read request binary: %+v, %v", rd, err)
	}
	dl, err := DecodeDownloadRequest([]byte(`{"src_path":"/x","binary":true}`))
	if err != nil || !dl.Binary {
		t.Errorf("download request binary: %+v, %v", dl, err)
	}
	old, err := DecodeDownloadRequest([]byte(`{"src_path":"/x"}`))
	if err != nil || old.Binary {
		t.Errorf("old download request must default to false: %+v, %v", old, err)
	}
}
