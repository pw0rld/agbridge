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
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func newDaemonCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the agent-side daemon (Phase 2: ping-pong responder)",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				if inner.Type == proto.FrameTypePing {
					pong, _ := proto.Frame{Type: proto.FrameTypePong, ReqID: inner.ReqID}.Encode()
					_ = conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: pong})
				}
			}
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to daemon YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}
