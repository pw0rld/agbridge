// Package auth provides cryptographic primitives used by the handshake
// and per-frame signing. All comparisons MUST be constant-time.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// SHA256Hex returns the lower-case hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SecretMatches reports whether the supplied secret hashes to storedHash.
// storedHash MUST carry an algorithm prefix; only "sha256:" is supported.
// Constant-time comparison.
func SecretMatches(secret, storedHash string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(storedHash, prefix) {
		return false
	}
	want := storedHash[len(prefix):]
	got := SHA256Hex([]byte(secret))
	if len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
