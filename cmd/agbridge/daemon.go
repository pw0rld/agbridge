package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/execproto"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/sandbox"
	"github.com/pw0rld/agbridge/internal/tools"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

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
				switch inner.Type {
				case proto.FrameTypePing:
					pong, _ := proto.Frame{Type: proto.FrameTypePong, ReqID: inner.ReqID}.Encode()
					_ = conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: pong})
				case proto.FrameTypeExecRequest:
					handleExecRequest(ctx, conn, inner, cfg)
				}
			}
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to daemon YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func handleExecRequest(ctx context.Context, conn *wss.Conn, inner proto.Frame, cfg *config.DaemonConfig) {
	req, err := execproto.DecodeExecRequest(inner.Payload)
	if err != nil {
		sendInner(ctx, conn, proto.Frame{Type: proto.FrameTypeError, ReqID: inner.ReqID, Payload: []byte("bad_payload")})
		return
	}
	onChunk := func(c execproto.ExecChunk) {
		chunkPayload, _ := c.Encode()
		sendInner(ctx, conn, proto.Frame{Type: proto.FrameTypeExecChunk, ReqID: inner.ReqID, Payload: chunkPayload})
	}
	complete, runErr := tools.Exec(ctx, req, cfg.AllowedExecCwds, cfg.EnvAllowlist, onChunk)
	if runErr != nil {
		code := "exec_failed"
		if runErr == tools.ErrCwdForbidden {
			code = "path_forbidden"
		}
		sendInner(ctx, conn, proto.Frame{Type: proto.FrameTypeError, ReqID: inner.ReqID, Payload: []byte(code)})
		return
	}
	completePayload, _ := complete.Encode()
	sendInner(ctx, conn, proto.Frame{Type: proto.FrameTypeExecComplete, ReqID: inner.ReqID, Payload: completePayload})
}

func sendInner(ctx context.Context, conn *wss.Conn, inner proto.Frame) {
	encoded, err := inner.Encode()
	if err != nil {
		return
	}
	_ = conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: encoded})
}
