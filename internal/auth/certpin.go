package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"strings"
)

var (
	ErrNoPeerCert   = errors.New("auth: TLS peer presented no certificate")
	ErrPinMismatch  = errors.New("auth: TLS cert pin mismatch")
	ErrBadPinFormat = errors.New("auth: pin must be sha256:<64-hex-chars>")
)

// CertFingerprintSHA256 returns the canonical pin string for a DER-encoded leaf cert.
func CertFingerprintSHA256(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// AttachCertPin installs a VerifyConnection callback on cfg that rejects any
// TLS handshake whose leaf cert SHA-256 doesn't match pin.
//
// pin format: "sha256:<64 hex chars>"
func AttachCertPin(cfg *tls.Config, pin string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(pin, prefix) || len(pin) != len(prefix)+64 {
		return ErrBadPinFormat
	}
	want, err := hex.DecodeString(pin[len(prefix):])
	if err != nil {
		return ErrBadPinFormat
	}
	cfg.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return ErrNoPeerCert
		}
		got := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(got[:], want) != 1 {
			return ErrPinMismatch
		}
		return nil
	}
	return nil
}
