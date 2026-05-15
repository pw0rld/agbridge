package pki

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestGenerateSelfSignedDefaults(t *testing.T) {
	out, err := GenerateSelfSigned(Options{})
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	if !strings.HasPrefix(out.Pin, "sha256:") || len(out.Pin) != len("sha256:")+64 {
		t.Errorf("pin malformed: %q", out.Pin)
	}
	if _, err := tls.X509KeyPair(out.CertPEM, out.KeyPEM); err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
}

func TestGenerateSelfSignedRespectsCN(t *testing.T) {
	out, err := GenerateSelfSigned(Options{CommonName: "gw.example.com"})
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	leaf := mustParseLeaf(t, out.CertPEM)
	if leaf.Subject.CommonName != "gw.example.com" {
		t.Errorf("CN: got %q want gw.example.com", leaf.Subject.CommonName)
	}
}

func TestGenerateSelfSignedValidDays(t *testing.T) {
	out, err := GenerateSelfSigned(Options{ValidDays: 7})
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	leaf := mustParseLeaf(t, out.CertPEM)
	until := time.Until(leaf.NotAfter)
	if until < 6*24*time.Hour || until > 8*24*time.Hour {
		t.Errorf("NotAfter: %v from now (want ~7 days)", until)
	}
}

func TestPinFromCertPEMRoundTrip(t *testing.T) {
	out, err := GenerateSelfSigned(Options{})
	if err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}
	pin, err := PinFromCertPEM(out.CertPEM)
	if err != nil {
		t.Fatalf("PinFromCertPEM: %v", err)
	}
	if pin != out.Pin {
		t.Errorf("pin mismatch: %q vs %q", pin, out.Pin)
	}
}

func TestPinFromCertPEMRejectsGarbage(t *testing.T) {
	if _, err := PinFromCertPEM([]byte("not a pem")); err == nil {
		t.Errorf("expected error on garbage input")
	}
}

func mustParseLeaf(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("decode PEM: nil or wrong type")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return leaf
}
