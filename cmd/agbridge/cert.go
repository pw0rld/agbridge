package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/pki"
)

// newCertCmd returns the `agbridge cert` parent command. Subcommand `gen`
// produces a self-signed ECDSA P-256 cert + key + prints the SHA-256 pin
// over the raw DER (the format AttachCertPin compares against).
func newCertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cert",
		Short: "TLS certificate helpers",
	}
	cmd.AddCommand(newCertGenCmd())
	return cmd
}

func newCertGenCmd() *cobra.Command {
	var (
		cn       string
		outDir   string
		days     int
		emitJSON bool
	)
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Generate a self-signed ECDSA P-256 cert + key, print the SHA-256 pin",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := pki.GenerateSelfSigned(pki.Options{
				CommonName: cn,
				ValidDays:  days,
			})
			if err != nil {
				return fmt.Errorf("generate: %w", err)
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", outDir, err)
			}
			certPath := filepath.Join(outDir, "cert.pem")
			keyPath := filepath.Join(outDir, "key.pem")
			if err := os.WriteFile(certPath, out.CertPEM, 0o600); err != nil {
				return fmt.Errorf("write cert: %w", err)
			}
			if err := os.WriteFile(keyPath, out.KeyPEM, 0o600); err != nil {
				return fmt.Errorf("write key: %w", err)
			}
			if emitJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
					"cert": certPath,
					"key":  keyPath,
					"pin":  out.Pin,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cert: %s\nkey:  %s\npin:  %s\n",
				certPath, keyPath, out.Pin)
			return nil
		},
	}
	cmd.Flags().StringVar(&cn, "cn", "agbridge", "Subject Common Name")
	cmd.Flags().StringVar(&outDir, "out", ".", "output directory")
	cmd.Flags().IntVar(&days, "days", 365, "validity period in days")
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit machine-readable JSON")
	return cmd
}
