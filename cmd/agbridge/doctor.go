package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pw0rld/agbridge/internal/state"
)

// newDoctorCmd runs a 5-stage connectivity probe (DNS / TCP / TLS / pin /
// HTTP reachability) using whatever gateway URL is pinned in state.json.
// Does NOT exchange tokens; safe to run on an already-enrolled device.
func newDoctorCmd() *cobra.Command {
	var stateDir string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe gateway connectivity from current state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if stateDir == "" {
				stateDir = defaultStateDir()
			}
			path := filepath.Join(stateDir, "state.json")
			var gwURL, pinned string
			if bs, err := state.LoadBridge(path); err == nil {
				gwURL = bs.Gateway.URL
				pinned = bs.Gateway.CertPin
			} else if ds, err := state.LoadDaemon(path); err == nil {
				gwURL = ds.Gateway.URL
				pinned = ds.Gateway.CertPin
			} else {
				return fmt.Errorf("no usable state at %s", path)
			}
			return doctorProbe(cmd.Context(), gwURL, pinned, cmd.OutOrStderr())
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "default ~/.config/agbridge/")
	return cmd
}

func doctorProbe(ctx context.Context, gwURL, expectedPin string, w io.Writer) error {
	u, err := url.Parse(gwURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid gateway URL: %v", err)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	step := func(stage string, ok bool, msg string, dur time.Duration) {
		mark := "✓"
		if !ok {
			mark = "✗"
		}
		fmt.Fprintf(w, "  [%s] %-20s %s\n", mark, stage, dur.Round(time.Millisecond))
		if msg != "" {
			fmt.Fprintf(w, "        %s\n", msg)
		}
	}

	t0 := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(ctx, hostnameOnly(host))
	if err != nil {
		step("dns_resolve", false, err.Error(), time.Since(t0))
		return err
	}
	step("dns_resolve", true, fmt.Sprintf("%d address(es)", len(addrs)), time.Since(t0))

	t0 = time.Now()
	c, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		step("tcp_connect", false, err.Error(), time.Since(t0))
		return err
	}
	_ = c.Close()
	step("tcp_connect", true, "", time.Since(t0))

	t0 = time.Now()
	tlsConn, err := tls.Dial("tcp", host, &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		step("tls_handshake", false, err.Error(), time.Since(t0))
		return err
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	_ = tlsConn.Close()
	step("tls_handshake", true, "TLS 1.3 OK", time.Since(t0))

	if len(certs) == 0 {
		return fmt.Errorf("no peer cert")
	}
	t0 = time.Now()
	sum := sha256.Sum256(certs[0].Raw)
	gotPin := "sha256:" + hex.EncodeToString(sum[:])
	if expectedPin != "" && gotPin != expectedPin {
		step("cert_pin", false, fmt.Sprintf("local=%s pinned=%s", gotPin, expectedPin), time.Since(t0))
		return fmt.Errorf("cert pin mismatch — possible MITM or legitimate rotation; re-enroll to update")
	}
	step("cert_pin", true, gotPin, time.Since(t0))

	t0 = time.Now()
	httpsURL := "https://" + host + "/v1/enroll"
	cl := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}},
		Timeout:   5 * time.Second,
	}
	resp, err := cl.Post(httpsURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		step("http_reachable", false, err.Error(), time.Since(t0))
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnauthorized {
		step("http_reachable", false, fmt.Sprintf("status %d", resp.StatusCode), time.Since(t0))
		return fmt.Errorf("unexpected status %d on /v1/enroll", resp.StatusCode)
	}
	step("http_reachable", true, "", time.Since(t0))

	fmt.Fprintln(w, "\nAll probes passed.")
	return nil
}
