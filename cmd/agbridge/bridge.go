package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/handshake"
	"github.com/pw0rld/agbridge/internal/proto"
	"github.com/pw0rld/agbridge/internal/transport"
	"github.com/pw0rld/agbridge/internal/transport/wss"
)

func newBridgeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Run the local MCP bridge (Phase 2: ping-pong harness)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadBridge(cfgPath)
			if err != nil {
				return err
			}
			tlsCfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}
			if err := auth.AttachCertPin(tlsCfg, cfg.CertPin); err != nil {
				return fmt.Errorf("cert pin: %w", err)
			}
			ctx := context.Background()
			conn, err := wss.Dial(ctx, cfg.GatewayURL, transport.Credentials{}, tlsCfg)
			if err != nil {
				return err
			}
			defer conn.Close()
			hello := handshake.Hello{Role: "bridge", Name: cfg.AgentName, Secret: cfg.APIKey, TargetDaemon: cfg.TargetDaemon}
			helloPayload, err := hello.Encode()
			if err != nil {
				return err
			}
			if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeHello, Payload: helloPayload}); err != nil {
				return err
			}
			ack, err := conn.Recv(ctx)
			if err != nil || ack.Type != proto.FrameTypeHelloAck {
				return fmt.Errorf("handshake failed: type=%v", ack.Type)
			}
			fmt.Fprintln(os.Stderr, "bridge: handshake ok, type req_id then enter to send a Ping")
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				reqID := scanner.Text()
				inner, _ := proto.Frame{Type: proto.FrameTypePing, ReqID: reqID}.Encode()
				signed := auth.SignFrame([]byte(cfg.APIKey), inner)
				if err := conn.Send(ctx, proto.Frame{Type: proto.FrameTypeRoute, Payload: signed}); err != nil {
					return err
				}
				resp, err := conn.Recv(ctx)
				if err != nil {
					return err
				}
				switch resp.Type {
				case proto.FrameTypeRoute:
					ri, err := proto.Decode(resp.Payload)
					if err != nil {
						return err
					}
					fmt.Printf("got Pong req_id=%s\n", ri.ReqID)
				case proto.FrameTypeError:
					fmt.Printf("got Error: %s\n", string(resp.Payload))
				default:
					fmt.Printf("got %v\n", resp.Type)
				}
			}
			return scanner.Err()
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to bridge YAML config")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}
