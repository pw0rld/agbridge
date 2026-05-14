package execproto

import (
	"encoding/base64"
	"testing"
)

func TestExecRequestRoundTrip(t *testing.T) {
	req := ExecRequest{
		Cmd:       "/bin/echo",
		Args:      []string{"hello", "world"},
		Cwd:       "/home/user/proj",
		Env:       map[string]string{"FOO": "bar"},
		TimeoutMs: 5000,
	}
	b, err := req.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeExecRequest(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Cmd != req.Cmd || got.Cwd != req.Cwd || got.TimeoutMs != req.TimeoutMs {
		t.Errorf("scalar mismatch: %+v vs %+v", got, req)
	}
	if len(got.Args) != 2 || got.Args[0] != "hello" {
		t.Errorf("args: %+v", got.Args)
	}
	if got.Env["FOO"] != "bar" {
		t.Errorf("env: %+v", got.Env)
	}
}

func TestExecChunkRoundTrip(t *testing.T) {
	c := ExecChunk{Stream: "stdout", Data: []byte("hello\n")}
	b, err := c.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeExecChunk(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Stream != "stdout" || string(got.Data) != "hello\n" {
		t.Errorf("got %+v", got)
	}
	if base64.StdEncoding.EncodeToString([]byte("hello\n")) == "" {
		t.Fatal("base64 sanity")
	}
}

func TestExecCompleteRoundTrip(t *testing.T) {
	c := ExecComplete{ExitCode: 0, DurationMs: 42, TimedOut: false, Truncated: true}
	b, err := c.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeExecComplete(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ExitCode != 0 || got.DurationMs != 42 || got.Truncated != true {
		t.Errorf("got %+v", got)
	}
}
