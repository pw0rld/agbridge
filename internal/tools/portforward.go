package tools

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/streamproto"
)

const (
	streamReadBufSize = 32 * 1024
	streamDialTimeout = 5 * time.Second
)

// StreamSender writes one inner frame back to the bridge. The daemon
// wrapper is responsible for wrapping it in a Route frame for transport.
type StreamSender func(inner proto.Frame) error

// HandleStreamOpen dials req.RemoteHost:req.RemotePort and shuttles bytes
// between the TCP connection and the WSS link. inbound delivers StreamData
// payloads from the bridge; sender emits StreamAck / StreamData / StreamClose
// inner frames. The function blocks until either side closes; the daemon
// dispatcher is expected to call it in a goroutine.
//
// forbidden lists ports that the daemon refuses to forward to (e.g. 22, 2375).
func HandleStreamOpen(ctx context.Context, req streamproto.StreamOpen, forbidden []int, inbound <-chan streamproto.StreamData, sender StreamSender) error {
	emitAck := func(ok bool, errStr string) error {
		ack := streamproto.StreamAck{StreamID: req.StreamID, Ok: ok, Err: errStr}
		payload, _ := ack.Encode()
		return sender(proto.Frame{Type: proto.FrameTypeStreamAck, ReqID: req.StreamID, Payload: payload})
	}

	for _, p := range forbidden {
		if p == req.RemotePort {
			return emitAck(false, "port_forbidden")
		}
	}

	addr := fmt.Sprintf("%s:%d", req.RemoteHost, req.RemotePort)
	conn, err := net.DialTimeout("tcp", addr, streamDialTimeout)
	if err != nil {
		return emitAck(false, err.Error())
	}
	if err := emitAck(true, ""); err != nil {
		_ = conn.Close()
		return err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		defer cancel()
		buf := make([]byte, streamReadBufSize)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				sd := streamproto.StreamData{StreamID: req.StreamID, Data: data}
				payload, _ := sd.Encode()
				if serr := sender(proto.Frame{Type: proto.FrameTypeStreamData, ReqID: req.StreamID, Payload: payload}); serr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		defer cancel()
		for {
			select {
			case <-streamCtx.Done():
				return
			case d, ok := <-inbound:
				if !ok {
					return
				}
				if _, err := conn.Write(d.Data); err != nil {
					return
				}
			}
		}
	}()

	<-streamCtx.Done()
	_ = conn.Close()

	closeFrame := streamproto.StreamClose{StreamID: req.StreamID}
	payload, _ := closeFrame.Encode()
	_ = sender(proto.Frame{Type: proto.FrameTypeStreamClose, ReqID: req.StreamID, Payload: payload})
	return nil
}
