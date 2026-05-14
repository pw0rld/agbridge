package auth

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/pw0rld/agbridge/internal/transport/testcerts"
)

func TestCertFingerprintAndPin(t *testing.T) {
	srvCfg, _ := testcerts.MustGenerate(t)
	leafDER := srvCfg.Certificates[0].Certificate[0]
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	fp := CertFingerprintSHA256(leafDER)
	if !strings.HasPrefix(fp, "sha256:") || len(fp) != len("sha256:")+64 {
		t.Fatalf("bad fingerprint format: %q", fp)
	}

	// Matching pin should accept (InsecureSkipVerify lets the callback govern).
	cli := &tls.Config{InsecureSkipVerify: true}
	if err := AttachCertPin(cli, fp); err != nil {
		t.Fatalf("attach: %v", err)
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err := cli.VerifyConnection(state); err != nil {
		t.Errorf("expected accept, got %v", err)
	}

	// Wrong pin should reject.
	cli2 := &tls.Config{InsecureSkipVerify: true}
	if err := AttachCertPin(cli2, "sha256:"+strings.Repeat("00", 32)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := cli2.VerifyConnection(state); err == nil {
		t.Errorf("expected reject, got nil")
	}
}
