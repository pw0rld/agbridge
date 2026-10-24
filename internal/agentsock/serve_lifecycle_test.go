package agentsock

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestServeRejectsSecondAgent pins the single-instance guard: a second
// Serve on the same path must fail instead of unlinking the live
// socket. Without the sidecar lock it would steal the path, and the
// loser's path-based cleanup would later delete the winner's socket.
func TestServeRejectsSecondAgent(t *testing.T) {
	h := func(context.Context, Request) (any, error) { return "ok", nil }
	sock := startServer(t, h, ServerOptions{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Serve(ctx, sock, h, ServerOptions{})
	if err == nil {
		t.Fatal("second Serve succeeded; want single-instance rejection")
	}
	if !strings.Contains(err.Error(), "another agent") {
		t.Fatalf("error = %v, want an 'another agent' diagnostic", err)
	}

	// The first agent must still own the socket and keep answering.
	if _, statErr := os.Stat(sock); statErr != nil {
		t.Fatalf("first agent's socket vanished: %v", statErr)
	}
	resp, err := call(t, sock, Request{ID: NewID(), Handler: "probe"})
	if err != nil {
		t.Fatalf("first agent stopped serving: %v", err)
	}
	if !resp.OK {
		t.Fatalf("probe failed: %+v", resp)
	}
}

// TestServeSocketModeIsPrivate verifies the socket is published with
// 0600 (the temporary-name bind means no window with umask perms).
func TestServeSocketModeIsPrivate(t *testing.T) {
	sock := startServer(t, func(context.Context, Request) (any, error) { return "ok", nil }, ServerOptions{})
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600", perm)
	}
}
