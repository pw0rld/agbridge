package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
)

// newKeygenCmd generates a random 32-byte secret and its SHA-256 hash.
// Output goes to stdout so callers can pipe / capture.
//
//	secret: <base64>   — goes in bridge.yaml api_key / daemon.yaml registration_token
//	hash:   sha256:hex — goes in gateway.yaml api_key_hash / token_hash
func newKeygenCmd() *cobra.Command {
	var emitJSON bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a random secret + its sha256 hash for agbridge configs",
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return fmt.Errorf("rand: %w", err)
			}
			secret := base64.StdEncoding.EncodeToString(raw[:])
			hash := "sha256:" + auth.SHA256Hex([]byte(secret))
			if emitJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
					"secret": secret,
					"hash":   hash,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret: %s\nhash:   %s\n", secret, hash)
			return nil
		},
	}
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit machine-readable JSON")
	return cmd
}
