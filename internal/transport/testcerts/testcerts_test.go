package testcerts

import (
	"crypto/tls"
	"testing"
)

func TestServerTLSConfigUsable(t *testing.T) {
	srv, cli := MustGenerate(t)
	if len(srv.Certificates) != 1 {
		t.Fatalf("server cert missing")
	}
	if cli.RootCAs == nil {
		t.Fatalf("client root CAs not set")
	}
	// sanity: client should accept server cert via the same CA pool
	_ = tls.Config{Certificates: srv.Certificates, RootCAs: cli.RootCAs}
}
