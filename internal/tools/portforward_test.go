package tools

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/streamproto"
)

func startEchoServer(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr)
}

type captureSender struct {
	mu     sync.Mutex
	frames []proto.Frame
}

func (s *captureSender) send(f proto.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, f)
	return nil
}

func (s *captureSender) snapshot() []proto.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.Frame, len(s.frames))
	copy(out, s.frames)
	return out
}

func (s *captureSender) firstOfType(t proto.FrameType) (proto.Frame, bool) {
	for _, f := range s.snapshot() {
		if f.Type == t {
			return f, true
		}
	}
	return proto.Frame{}, false
}

func waitFor(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", msg)
}

func TestPortForwardHappy(t *testing.T) {
	addr := startEchoServer(t)
	cap := &captureSender{}
	inbound := make(chan streamproto.StreamData, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = HandleStreamOpen(ctx,
			streamproto.StreamOpen{StreamID: "s1", RemoteHost: "127.0.0.1", RemotePort: addr.Port},
			nil, inbound, cap.send)
		close(done)
	}()

	waitFor(t, func() bool {
		f, ok := cap.firstOfType(proto.FrameTypeStreamAck)
		if !ok {
			return false
		}
		ack, _ := streamproto.DecodeStreamAck(f.Payload)
		return ack.Ok
	}, 2*time.Second, "StreamAck ok=true")

	inbound <- streamproto.StreamData{StreamID: "s1", Data: []byte("hello")}

	waitFor(t, func() bool {
		for _, f := range cap.snapshot() {
			if f.Type == proto.FrameTypeStreamData {
				sd, _ := streamproto.DecodeStreamData(f.Payload)
				if string(sd.Data) == "hello" {
					return true
				}
			}
		}
		return false
	}, 2*time.Second, "echoed StreamData")

	close(inbound)
	<-done

	closeFrame, ok := cap.firstOfType(proto.FrameTypeStreamClose)
	if !ok {
		t.Fatal("missing StreamClose frame")
	}
	sc, _ := streamproto.DecodeStreamClose(closeFrame.Payload)
	if sc.StreamID != "s1" {
		t.Errorf("close stream_id: %q", sc.StreamID)
	}
}

func TestPortForwardForbiddenPort(t *testing.T) {
	cap := &captureSender{}
	inbound := make(chan streamproto.StreamData)
	err := HandleStreamOpen(context.Background(),
		streamproto.StreamOpen{StreamID: "s1", RemoteHost: "127.0.0.1", RemotePort: 22},
		[]int{22, 2375}, inbound, cap.send)
	if err != nil {
		t.Fatalf("HandleStreamOpen: %v", err)
	}
	f, ok := cap.firstOfType(proto.FrameTypeStreamAck)
	if !ok {
		t.Fatal("expected StreamAck")
	}
	ack, _ := streamproto.DecodeStreamAck(f.Payload)
	if ack.Ok {
		t.Error("expected ok=false")
	}
	if ack.Err != "port_forbidden" {
		t.Errorf("err: %q", ack.Err)
	}
}

func TestPortForwardDialFailure(t *testing.T) {
	cap := &captureSender{}
	inbound := make(chan streamproto.StreamData)
	err := HandleStreamOpen(context.Background(),
		streamproto.StreamOpen{StreamID: "s1", RemoteHost: "127.0.0.1", RemotePort: 1},
		nil, inbound, cap.send)
	if err != nil {
		t.Fatalf("HandleStreamOpen: %v", err)
	}
	f, ok := cap.firstOfType(proto.FrameTypeStreamAck)
	if !ok {
		t.Fatal("expected StreamAck")
	}
	ack, _ := streamproto.DecodeStreamAck(f.Payload)
	if ack.Ok {
		t.Error("expected ok=false on dial failure")
	}
	if ack.Err == "" {
		t.Error("expected non-empty err")
	}
}
