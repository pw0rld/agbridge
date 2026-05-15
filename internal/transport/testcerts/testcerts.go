// Package testcerts produces self-signed TLS material for tests only.
// Never use in production paths — for production cert generation use
// internal/pki (or the `agbridge cert gen` subcommand).
package testcerts

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/pw0rld/agbridge/internal/pki"
)

// MustGenerate returns a (serverTLS, clientTLS) pair backed by a fresh
// self-signed cert. ClientTLS trusts the server cert via a private CA pool.
func MustGenerate(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	out, err := pki.GenerateSelfSigned(pki.Options{
		CommonName: "agbridge-test",
		ValidDays:  1,
	})
	if err != nil {
		t.Fatalf("pki.GenerateSelfSigned: %v", err)
	}
	pair, err := tls.X509KeyPair(out.CertPEM, out.KeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(out.CertPEM) {
		t.Fatalf("AppendCertsFromPEM: rejected our own cert")
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13},
		&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
}
