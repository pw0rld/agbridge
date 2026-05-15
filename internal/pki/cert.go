// Package pki produces self-signed TLS material for agbridge gateways.
// Used by the `agbridge cert gen` CLI subcommand and by the test-only
// transport/testcerts wrapper. Generates ECDSA P-256 keys (Go 1.25+ tls
// negotiates TLS 1.3 with these by default) and computes the SHA-256 pin
// over the raw DER, which is what bridge/daemon AttachCertPin compares.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"time"
)

// SelfSigned is the result of GenerateSelfSigned: PEM-encoded cert and key
// plus the "sha256:<hex>" pin string (matches the format AttachCertPin
// expects in YAML).
type SelfSigned struct {
	CertPEM []byte
	KeyPEM  []byte
	Pin     string
}

// Options control GenerateSelfSigned. Zero values fill in sensible defaults
// (CN=agbridge, ValidDays=365, DNSNames+IPAddresses cover localhost).
type Options struct {
	CommonName  string
	ValidDays   int
	DNSNames    []string
	IPAddresses []net.IP
}

// GenerateSelfSigned creates a fresh ECDSA P-256 keypair and a self-signed
// certificate. The cert is marked CA + server/client auth so it can be
// pinned and used directly (no separate CA needed for agbridge's threat
// model).
func GenerateSelfSigned(opts Options) (*SelfSigned, error) {
	if opts.CommonName == "" {
		opts.CommonName = "agbridge"
	}
	if opts.ValidDays <= 0 {
		opts.ValidDays = 365
	}
	if len(opts.DNSNames) == 0 {
		opts.DNSNames = []string{"localhost"}
	}
	if len(opts.IPAddresses) == 0 {
		opts.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: opts.CommonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Duration(opts.ValidDays) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           opts.IPAddresses,
		DNSNames:              opts.DNSNames,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(der)
	return &SelfSigned{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		Pin:     "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// PinFromCertPEM computes the same "sha256:<hex>" pin string from an
// existing PEM-encoded certificate. Used by callers (and tests) that want
// to verify a deployed cert matches the pin a config expects.
func PinFromCertPEM(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("pki: not a CERTIFICATE PEM block")
	}
	sum := sha256.Sum256(block.Bytes)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
