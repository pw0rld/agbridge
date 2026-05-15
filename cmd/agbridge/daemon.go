package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/dialer"
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

			for {
				if ctx.Err() != nil {
					return nil
				}
				var conn *wss.Conn
				if err := dialer.Loop(ctx, func(c context.Context) error {
					cc, derr := daemonDialAndHandshake(c, cfg, tlsCfg)
					if derr != nil {
						return derr
					}
					conn = cc
					return nil
				}, dialer.Options{}); err != nil {
					return nil
				}
				fmt.Fprintln(os.Stderr, "daemon: handshake ok, awaiting Route frames")
				runDaemonSession(ctx, conn, cfg)
				_ = conn.Close()
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintln(os.Stderr, "daemon: connection lost, reconnecting…")
			}
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to daemon YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

// daemonDialAndHandshake dials the gateway and runs the daemon-side
// handshake. On any failure the new conn is closed.
func daemonDialAndHandshake(ctx context.Context, cfg *config.DaemonConfig, tlsCfg *tls.Config) (*wss.Conn, error) {
	conn, err := wss.Dial(ctx, cfg.GatewayURL, transport.Credentials{}, tlsCfg)
	if err != nil {
		return nil, err
	}
	hello := handshake.Hello{Role: "daemon", Name: cfg.DaemonName, Secret: cfg.RegistrationToken}
	helloPayload, _ := hello.Encode()
	if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: helloPayload}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	ack, err := conn.Recv(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if ack.Type != proto.FrameTypeHelloAck {
		_ = conn.Close()
		return nil, fmt.Errorf("handshake nack: %v", ack.Type)
	}
	return conn, nil
}

// runDaemonSession dispatches Route frames on conn until Recv errors or the
// parent ctx cancels. A per-session ctx is derived and cancelled before
// return so in-flight tool goroutines (handleExec / handleWrite /
// handleStreamOpen) exit promptly rather than leaking until parent cancel.
func runDaemonSession(parent context.Context, conn *wss.Conn, cfg *config.DaemonConfig) {
	sessionCtx, cancel := context.WithCancel(parent)
	defer cancel()
	state := &daemonState{
		cfg:     cfg,
		conn:    conn,
		writes:  make(map[string]*writeSlot),
		streams: make(map[string]*streamSlot),
	}
	for {
		f, err := conn.Recv(sessionCtx)
		if err != nil {
			return
		}
		if f.Type != proto.FrameTypeRoute {
			continue
		}
		inner, err := proto.Decode(f.Payload)
		if err != nil {
			continue
		}
		state.dispatch(sessionCtx, inner)
	}
}

type writeSlot struct {
	inbound chan fileproto.FileChunk
}

type streamSlot struct {
	inbound chan streamproto.StreamData
	cancel  context.CancelFunc
}

type daemonState struct {
	cfg     *config.DaemonConfig
	conn    *wss.Conn
	mu      sync.Mutex
	writes  map[string]*writeSlot
	streams map[string]*streamSlot
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
		slot := s.beginWrite(inner.ReqID)
		go s.handleWrite(ctx, inner, slot)
	case proto.FrameTypeFileChunk:
		s.deliverFileChunk(ctx, inner)
	case proto.FrameTypeStreamOpen:
		req, err := streamproto.DecodeStreamOpen(inner.Payload)
		if err != nil {
			return
		}
		slot, streamCtx := s.beginStream(ctx, req.StreamID)
		go s.handleStreamOpen(streamCtx, req, slot)
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

func (s *daemonState) beginWrite(reqID string) *writeSlot {
	slot := &writeSlot{inbound: make(chan fileproto.FileChunk, 8)}
	s.mu.Lock()
	s.writes[reqID] = slot
	s.mu.Unlock()
	return slot
}

func (s *daemonState) handleWrite(ctx context.Context, inner proto.Frame, slot *writeSlot) {
	defer func() {
		s.mu.Lock()
		delete(s.writes, inner.ReqID)
		s.mu.Unlock()
	}()
	req, err := fileproto.DecodeFileWriteRequest(inner.Payload)
	if err != nil {
		s.emitFileComplete(ctx, inner.ReqID, fileproto.FileComplete{Err: "bad_payload"})
		return
	}
	next := func() (fileproto.FileChunk, error) {
		select {
		case c := <-slot.inbound:
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
	slot, ok := s.writes[inner.ReqID]
	s.mu.Unlock()
	if !ok {
		return
	}
	c, err := fileproto.DecodeFileChunk(inner.Payload)
	if err != nil {
		return
	}
	select {
	case slot.inbound <- c:
	case <-ctx.Done():
	}
}

func (s *daemonState) beginStream(parent context.Context, streamID string) (*streamSlot, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	slot := &streamSlot{inbound: make(chan streamproto.StreamData, 64), cancel: cancel}
	s.mu.Lock()
	s.streams[streamID] = slot
	s.mu.Unlock()
	return slot, ctx
}

func (s *daemonState) handleStreamOpen(ctx context.Context, req streamproto.StreamOpen, slot *streamSlot) {
	defer func() {
		s.mu.Lock()
		delete(s.streams, req.StreamID)
		s.mu.Unlock()
		slot.cancel()
	}()
	sender := func(f proto.Frame) error {
		s.sendInner(ctx, f)
		return nil
	}
	_ = tools.HandleStreamOpen(ctx, req, s.cfg.ForbiddenPorts, slot.inbound, sender)
}

func (s *daemonState) deliverStreamData(ctx context.Context, inner proto.Frame) {
	s.mu.Lock()
	slot, ok := s.streams[inner.ReqID]
	s.mu.Unlock()
	if !ok {
		return
	}
	d, err := streamproto.DecodeStreamData(inner.Payload)
	if err != nil {
		return
	}
	select {
	case slot.inbound <- d:
	case <-ctx.Done():
	}
}

func (s *daemonState) closeStream(streamID string) {
	s.mu.Lock()
	slot, ok := s.streams[streamID]
	s.mu.Unlock()
	if ok {
		slot.cancel()
	}
}
