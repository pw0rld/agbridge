package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/dialer"
	"github.com/pw0rld/agbridge/internal/e2e"
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

			// Load Noise dependencies once (reused across reconnects).
			deps, err := loadDaemonE2EDeps(cfg)
			if err != nil {
				return err
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
				runDaemonSession(ctx, conn, cfg, deps)
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

// daemonE2EDeps is the cached E2E configuration shared across reconnects.
// Nil when cfg.E2EMode == "disabled".
type daemonE2EDeps struct {
	noiseKey    *e2e.StaticKey
	allowedPubs [][]byte
	prologue    []byte
	mode        string // "optional" | "required"
}

// loadDaemonE2EDeps validates and loads Noise key material + ACL once at
// startup. Returns nil deps for e2e_mode="disabled".
func loadDaemonE2EDeps(cfg *config.DaemonConfig) (*daemonE2EDeps, error) {
	if cfg.E2EMode == "disabled" {
		return nil, nil
	}
	sk, err := e2e.LoadStatic(cfg.NoiseStaticKeyPath)
	if err != nil {
		return nil, fmt.Errorf("daemon: load noise static key %q: %w", cfg.NoiseStaticKeyPath, err)
	}
	var pubs [][]byte
	for i, b64 := range cfg.AllowedBridgePubkeys {
		pub, err := e2e.PubFromBase64(b64)
		if err != nil {
			return nil, fmt.Errorf("daemon: allowed_bridge_pubkeys[%d]: %w", i, err)
		}
		pubs = append(pubs, pub)
	}
	return &daemonE2EDeps{
		noiseKey:    sk,
		allowedPubs: pubs,
		prologue:    []byte(fmt.Sprintf("agbridge/v1|%s|%s", cfg.GatewayURL, cfg.DaemonName)),
		mode:        cfg.E2EMode,
	}, nil
}

// runDaemonSession dispatches Route frames on conn until Recv errors or the
// parent ctx cancels. A per-session ctx is derived and cancelled before
// return so in-flight tool goroutines (handleExec / handleWrite /
// handleStreamOpen) exit promptly rather than leaking until parent cancel.
//
// gorilla/websocket.ReadMessage doesn't honor a Go ctx, so we install a
// watchdog goroutine that closes the conn when sessionCtx cancels —
// otherwise SIGTERM would leave the daemon parked in Recv until the next
// frame arrived.
func runDaemonSession(parent context.Context, conn *wss.Conn, cfg *config.DaemonConfig, deps *daemonE2EDeps) {
	sessionCtx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		<-sessionCtx.Done()
		_ = conn.Close()
	}()
	state := &daemonState{
		cfg:     cfg,
		conn:    conn,
		e2e:     deps,
		writes:  make(map[string]*writeSlot),
		streams: make(map[string]*streamSlot),
	}
	for {
		f, err := conn.Recv(sessionCtx)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.FrameTypeNoiseInit:
			state.handleNoiseInit(sessionCtx, f)
			continue
		case proto.FrameTypeRoute:
			innerBytes, err := state.unwrapRoute(f.Payload)
			if err != nil {
				continue
			}
			inner, err := proto.Decode(innerBytes)
			if err != nil {
				continue
			}
			state.dispatch(sessionCtx, inner)
		default:
			// ignore stray frames
		}
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
	e2e     *daemonE2EDeps // nil when e2e_mode == "disabled"
	mu      sync.Mutex
	writes  map[string]*writeSlot
	streams map[string]*streamSlot

	// Active E2E session. v0.1.0 single-active-session per daemon conn
	// (v0.0.8 will demux per-bridge by session_id when multi-bridge lands).
	sessionMu sync.Mutex
	session   *e2e.Session
	sessionID [8]byte
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
	s.sessionMu.Lock()
	sess := s.session
	sid := s.sessionID
	s.sessionMu.Unlock()

	var payload []byte
	if sess != nil {
		ct, err := sess.Encrypt(sid[:], encoded)
		if err != nil {
			return
		}
		payload = make([]byte, 8+len(ct))
		copy(payload, sid[:])
		copy(payload[8:], ct)
	} else {
		payload = encoded
	}
	_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: payload})
}

// unwrapRoute extracts the inner frame bytes from a Route payload. If a
// session is active the payload is AEAD-decrypted (after stripping the
// 8-byte session_id prefix). When e2e_mode == "required" and no session
// exists, plaintext Route is rejected outright.
func (s *daemonState) unwrapRoute(payload []byte) ([]byte, error) {
	s.sessionMu.Lock()
	sess := s.session
	sid := s.sessionID
	s.sessionMu.Unlock()

	if sess == nil {
		if s.e2e != nil && s.e2e.mode == "required" {
			return nil, errors.New("plaintext Route refused under e2e_mode=required")
		}
		return payload, nil
	}
	if len(payload) < 8 {
		return nil, errors.New("route payload too short for sid")
	}
	var got [8]byte
	copy(got[:], payload[:8])
	if got != sid {
		return nil, errors.New("route sid mismatch")
	}
	plain, err := sess.Decrypt(sid[:], payload[8:])
	if err != nil {
		return nil, fmt.Errorf("aead decrypt: %w", err)
	}
	return plain, nil
}

// handleNoiseInit runs the responder side of Noise IK. On success it
// installs the resulting Session for subsequent Route frames. On any
// failure it returns an opaque "handshake_failed" Error frame (detailed
// reason is logged locally only to avoid leaking ACL info).
func (s *daemonState) handleNoiseInit(ctx context.Context, f proto.Frame) {
	if s.e2e == nil {
		_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: f.ReqID, Payload: []byte("e2e_disabled")})
		return
	}
	sidBytes, err := hex.DecodeString(f.ReqID)
	if err != nil || len(sidBytes) != 8 {
		fmt.Fprintf(os.Stderr, "daemon: NoiseInit bad sid: %v\n", err)
		_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: f.ReqID, Payload: []byte("handshake_failed")})
		return
	}
	resp, err := e2e.NewResponder(s.e2e.noiseKey, s.e2e.prologue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: NewResponder: %v\n", err)
		_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: f.ReqID, Payload: []byte("handshake_failed")})
		return
	}
	peer, err := resp.ReadMessage1(f.Payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: NoiseInit ReadMessage1: %v\n", err)
		_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: f.ReqID, Payload: []byte("handshake_failed")})
		return
	}
	if !s.peerAllowed(peer) {
		fmt.Fprintf(os.Stderr, "daemon: NoiseInit peer not in allowlist\n")
		_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: f.ReqID, Payload: []byte("handshake_failed")})
		return
	}
	msg2, sess, err := resp.WriteMessage2()
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon: WriteMessage2: %v\n", err)
		_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeError, ReqID: f.ReqID, Payload: []byte("handshake_failed")})
		return
	}
	var sid [8]byte
	copy(sid[:], sidBytes)

	s.sessionMu.Lock()
	s.session = sess
	s.sessionID = sid
	s.sessionMu.Unlock()

	_ = s.conn.Send(ctx, proto.Frame{Type: proto.FrameTypeNoiseResp, ReqID: f.ReqID, Payload: msg2})
}

func (s *daemonState) peerAllowed(peer []byte) bool {
	if s.e2e == nil {
		return false
	}
	for _, a := range s.e2e.allowedPubs {
		if bytes.Equal(a, peer) {
			return true
		}
	}
	return false
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
