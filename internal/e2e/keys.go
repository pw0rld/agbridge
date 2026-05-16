// Package e2e implements bridge↔daemon end-to-end encryption via Noise IK
// over X25519 + ChaCha20-Poly1305 + BLAKE2s. Gateway sees only ciphertext.
package e2e

import (
	"encoding/base64"
	"errors"
	"os"

	"github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

// CipherSuite is the fixed Noise suite used by all agbridge sessions.
var CipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// StaticKey is an X25519 long-term keypair held by bridge or daemon.
type StaticKey struct {
	dh noise.DHKey
}

// GenerateStatic generates a fresh X25519 keypair from crypto/rand.
func GenerateStatic() (*StaticKey, error) {
	dh, err := CipherSuite.GenerateKeypair(nil)
	if err != nil {
		return nil, err
	}
	return &StaticKey{dh: dh}, nil
}

// Pub returns a copy of the 32-byte public key.
func (k *StaticKey) Pub() []byte {
	out := make([]byte, len(k.dh.Public))
	copy(out, k.dh.Public)
	return out
}

// RawPriv returns a copy of the 32-byte private key. Caller must keep secret.
func (k *StaticKey) RawPriv() []byte {
	out := make([]byte, len(k.dh.Private))
	copy(out, k.dh.Private)
	return out
}

// PubBase64 returns the public key as standard base64.
func (k *StaticKey) PubBase64() string {
	return base64.StdEncoding.EncodeToString(k.dh.Public)
}

// Save writes the raw 32-byte private key to path with 0600 permissions.
// The public half is re-derived on Load via X25519 scalar mult.
func (k *StaticKey) Save(path string) error {
	return os.WriteFile(path, k.dh.Private, 0o600)
}

// LoadStatic reads a 32-byte private key from path and derives the public half.
func LoadStatic(path string) (*StaticKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, errors.New("e2e: key file must be exactly 32 bytes")
	}
	pub, err := curve25519.X25519(data, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	return &StaticKey{dh: noise.DHKey{Private: data, Public: pub}}, nil
}

// PubFromBase64 parses a base64-encoded X25519 public key.
func PubFromBase64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("e2e: public key must be 32 bytes")
	}
	return b, nil
}
