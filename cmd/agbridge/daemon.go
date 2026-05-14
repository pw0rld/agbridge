package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/fileproto"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/sandbox"
	"github.com/pw0rld/agbridge/internal/streamproto"
	"github.com/pw0rld/agbridge/internal/tools"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

const maxFileBytes = 10 << 20

func newDaemonCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the agent-side daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sandbox.RefuseRoot(); err != nil {
				return err
			}
			cfg, err := config.LoadDaemon(cfgPath)
			if err != nil {
				return err
			}
			tlsCfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
			if err := auth.AttachCertPin(tlsCfg, cfg.CertPin); err != nil {
				return fmt.Errorf("cert pin: %w", err)
			}
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			conn, err := wss.Dial(ctx, cfg.GatewayURL, transport.Credentials{}, tlsCfg)
			if err != nil {
				return err
			}
			defer conn.Close()

			hello := handshake.Hello{Role: "daemon", Name: cfg.DaemonName, Secret: cfg.RegistrationToken}
			helloPayload, _ := hello.Encode()
			if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: helloPayload}); err != nil {
				return err
			}
			ack, err := conn.Recv(ctx)
			if err != nil || ack.Type != proto.FrameTypeHelloAck {
				return fmt.Errorf("handshake failed: %v", ack.Type)
			}
			fmt.Fprintln(os.Stderr, "daemon: handshake ok, awaiting Route frames")

			state := &daemonState{
				cfg:     cfg,
				conn:    conn,
				writes:  make(map[string]chan fileproto.FileChunk),
				streams: make(map[string]chan streamproto.StreamData),
			}
			for {
				f, err := conn.Recv(ctx)
				if err != nil {
					return err
				}
				if f.Type != proto.FrameTypeRoute {
					continue
				}
				inner, err := proto.Decode(f.Payload)
				if err != nil {
					continue
				}
				state.dispatch(ctx, inner)
			}
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to daemon YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

type daemonState struct {
	cfg     *config.DaemonConfig
	conn    *wss.Conn
	mu      sync.Mutex
	writes  map[string]chan fileproto.FileChunk
	streams map[string]chan streamproto.StreamData
}

func (s *daemonState) dispatch(ctx context.Context, inner proto.Frame) {
	switch inner.Type {
	case proto.FrameTypePing:
		s.sendInner(ctx, proto.Frame{Type: proto.FrameTypePong, ReqID: inner.ReqID})
	case proto.FrameTypeExecRequest:
		go s.handleExec(ctx, inner)
	case proto.FrameTypeFileReadRequest:
		go s.handleRead(ctx, inner)
	case proto.FrameTypeFileWriteRequest:
		go s.handleWrite(ctx, inner)
	case proto.FrameTypeFileChunk:
		s.deliverFileChunk(ctx, inner)
	case proto.FrameTypeStreamOpen:
		go s.handleStreamOpen(ctx, inner)
	case proto.FrameTypeStreamData:
		s.deliverStreamData(ctx, inner)
	case proto.FrameTypeStreamClose:
		s.closeStream(inner.ReqID)
	}
}

func (s *daemonState) sendInner(ctx context.Context, inner proto.Frame) {
	encoded, err := inner.Encode()
	if err != nil {
		return
	}
	_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: encoded})
}

func (s *daemonState) handleExec(ctx context.Context, inner proto.Frame) {
	req, err := execproto.DecodeExecRequest(inner.Payload)
	if err != nil {
		s.sendInner(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: inner.ReqID, Payload: []byte("bad_payload")})
		return
	}
	onChunk := func(c execproto.ExecChunk) {
		chunkPayload, _ := c.Encode()
		s.sendInner(ctx, proto.Frame{Type: proto.FrameTypeExecChunk, ReqID: inner.ReqID, Payload: chunkPayload})
	}
	complete, runErr := tools.Exec(ctx, req, s.cfg.AllowedExecCwds, s.cfg.EnvAllowlist, onChunk)
	if runErr != nil {
		code := "exec_failed"
		if runErr == tools.ErrCwdForbidden {
			code = "path_forbidden"
		}
		s.sendInner(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: inner.ReqID, Payload: []byte(code)})
		return
	}
	completePayload, _ := complete.Encode()
	s.sendInner(ctx, proto.Frame{Type: proto.FrameTypeExecComplete, ReqID: inner.ReqID, Payload: completePayload})
}

func (s *daemonState) handleRead(ctx context.Context, inner proto.Frame) {
	req, err := fileproto.DecodeFileReadRequest(inner.Payload)
	if err != nil {
		s.emitFileComplete(ctx, inner.ReqID, fileproto.FileComplete{Err: "bad_payload"})
		return
	}
	onChunk := func(c fileproto.FileChunk) {
		payload, _ := c.Encode()
		s.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileChunk, ReqID: inner.ReqID, Payload: payload})
	}
	complete, _ := tools.ReadFile(ctx, req, s.cfg.AllowedReadPaths, maxFileBytes, onChunk)
	s.emitFileComplete(ctx, inner.ReqID, complete)
}

func (s *daemonState) handleWrite(ctx context.Context, inner proto.Frame) {
	req, err := fileproto.DecodeFileWriteRequest(inner.Payload)
	if err != nil {
		s.emitFileComplete(ctx, inner.ReqID, fileproto.FileComplete{Err: "bad_payload"})
		return
	}
	ch := make(chan fileproto.FileChunk, 8)
	s.mu.Lock()
	s.writes[inner.ReqID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.writes, inner.ReqID)
		s.mu.Unlock()
	}()

	next := func() (fileproto.FileChunk, error) {
		select {
		case c, ok := <-ch:
			if !ok {
				return fileproto.FileChunk{}, io.ErrUnexpectedEOF
			}
			return c, nil
		case <-ctx.Done():
			return fileproto.FileChunk{}, ctx.Err()
		}
	}
	complete, _ := tools.WriteFile(ctx, req, s.cfg.AllowedWritePaths, maxFileBytes, next)
	s.emitFileComplete(ctx, inner.ReqID, complete)
}

func (s *daemonState) emitFileComplete(ctx context.Context, reqID string, c fileproto.FileComplete) {
	payload, _ := c.Encode()
	s.sendInner(ctx, proto.Frame{Type: proto.FrameTypeFileComplete, ReqID: reqID, Payload: payload})
}

func (s *daemonState) deliverFileChunk(ctx context.Context, inner proto.Frame) {
	s.mu.Lock()
	ch, ok := s.writes[inner.ReqID]
	s.mu.Unlock()
	if !ok {
		return
	}
	c, err := fileproto.DecodeFileChunk(inner.Payload)
	if err != nil {
		return
	}
	select {
	case ch <- c:
	case <-ctx.Done():
	}
}

func (s *daemonState) handleStreamOpen(ctx context.Context, inner proto.Frame) {
	req, err := streamproto.DecodeStreamOpen(inner.Payload)
	if err != nil {
		return
	}
	ch := make(chan streamproto.StreamData, 64)
	s.mu.Lock()
	s.streams[req.StreamID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.streams, req.StreamID)
		s.mu.Unlock()
	}()
	sender := func(f proto.Frame) error {
		s.sendInner(ctx, f)
		return nil
	}
	_ = tools.HandleStreamOpen(ctx, req, s.cfg.ForbiddenPorts, ch, sender)
}

func (s *daemonState) deliverStreamData(ctx context.Context, inner proto.Frame) {
	s.mu.Lock()
	ch, ok := s.streams[inner.ReqID]
	s.mu.Unlock()
	if !ok {
		return
	}
	d, err := streamproto.DecodeStreamData(inner.Payload)
	if err != nil {
		return
	}
	select {
	case ch <- d:
	case <-ctx.Done():
	}
}

func (s *daemonState) closeStream(streamID string) {
	s.mu.Lock()
	ch, ok := s.streams[streamID]
	if ok {
		delete(s.streams, streamID)
	}
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}
