package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/e2e"
)

// newKeygenCmd generates key material for agbridge configs.
//
//	--type secret (default): random 32-byte secret + sha256 hash for api_key /
//	  registration_token slots (paired with gateway.yaml hash slots).
//	--type noise: X25519 keypair for Noise IK E2E. The private key is saved
//	  to --out (or printed base64 if --out empty), public is printed base64.
func newKeygenCmd() *cobra.Command {
	var (
		emitJSON bool
		keyType  string
		outPath  string
	)
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a secret+hash or an X25519 keypair for agbridge configs",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch keyType {
			case "", "secret":
				return runKeygenSecret(cmd, emitJSON)
			case "noise":
				return runKeygenNoise(cmd, emitJSON, outPath)
			default:
				return fmt.Errorf("--type %q not recognised (want secret|noise)", keyType)
			}
		},
	}
	cmd.Flags().BoolVar(&emitJSON, "json", false, "emit machine-readable JSON")
	cmd.Flags().StringVar(&keyType, "type", "secret", "key kind to generate: secret | noise")
	cmd.Flags().StringVar(&outPath, "out", "", "(noise only) path to write the 32-byte private key with 0600 perms")
	return cmd
}

func runKeygenSecret(cmd *cobra.Command, emitJSON bool) error {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Errorf("rand: %w", err)
	}
	secret := base64.StdEncoding.EncodeToString(raw[:])
	hash := "sha256:" + auth.SHA256Hex([]byte(secret))
	if emitJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
			"type":   "secret",
			"secret": secret,
			"hash":   hash,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "secret: %s\nhash:   %s\n", secret, hash)
	return nil
}

func runKeygenNoise(cmd *cobra.Command, emitJSON bool, outPath string) error {
	sk, err := e2e.GenerateStatic()
	if err != nil {
		return fmt.Errorf("noise keygen: %w", err)
	}
	pubB64 := sk.PubBase64()
	privB64 := base64.StdEncoding.EncodeToString(sk.RawPriv())

	if outPath != "" {
		if err := sk.Save(outPath); err != nil {
			return fmt.Errorf("save noise key %q: %w", outPath, err)
		}
	}

	if emitJSON {
		out := map[string]string{
			"type":       "noise",
			"private_b64": privB64,
			"public_b64":  pubB64,
		}
		if outPath != "" {
			out["saved_to"] = outPath
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "type:        noise (X25519)\n")
	fmt.Fprintf(cmd.OutOrStdout(), "private_b64: %s\n", privB64)
	fmt.Fprintf(cmd.OutOrStdout(), "public_b64:  %s\n", pubB64)
	if outPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "saved_to:    %s (mode 0600)\n", outPath)
	}
	return nil
}
