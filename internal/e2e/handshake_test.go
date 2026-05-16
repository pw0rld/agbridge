package e2e

import (
	"bytes"
	"testing"
)

// IK msg1 with empty payload:
//   e   (32B ephemeral pub)
// + s   (32B static pub, encrypted, +16B tag = 48B on wire)
// + payload (0B + 16B tag)
// = 96 bytes
const ikMsg1EmptyPayloadLen = 96

// IK msg2 with empty payload:
//   e   (32B ephemeral pub)
// + payload (0B + 16B tag)
// = 48 bytes
const ikMsg2EmptyPayloadLen = 48

func TestInitiatorWriteMessage1Length(t *testing.T) {
	bridge, _ := GenerateStatic()
	daemon, _ := GenerateStatic()
	prologue := []byte("ag/v1|test|lab01")

	init, err := NewInitiator(bridge, daemon.Pub(), prologue)
	if err != nil {
		t.Fatal(err)
	}
	msg1, err := init.WriteMessage1()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg1) != ikMsg1EmptyPayloadLen {
		t.Fatalf("msg1 len = %d, want %d", len(msg1), ikMsg1EmptyPayloadLen)
	}
}

func TestNewInitiatorRejectsBadInputs(t *testing.T) {
	bridge, _ := GenerateStatic()
	if _, err := NewInitiator(nil, bridge.Pub(), nil); err == nil {
		t.Fatal("expected error for nil local")
	}
	if _, err := NewInitiator(bridge, []byte("short"), nil); err == nil {
		t.Fatal("expected error for short remote static")
	}
}

func TestResponderReadsPeerStatic(t *testing.T) {
	bridge, _ := GenerateStatic()
	daemon, _ := GenerateStatic()
	prologue := []byte("ag/v1|test|lab01")

	init, _ := NewInitiator(bridge, daemon.Pub(), prologue)
	msg1, _ := init.WriteMessage1()

	resp, err := NewResponder(daemon, prologue)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := resp.ReadMessage1(msg1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(peer, bridge.Pub()) {
		t.Fatal("responder did not recover bridge static pub")
	}
	// PeerStatic accessor should return same value.
	if !bytes.Equal(resp.PeerStatic(), bridge.Pub()) {
		t.Fatal("PeerStatic accessor mismatch")
	}
}

func TestResponderRejectsBadMsg1(t *testing.T) {
	daemon, _ := GenerateStatic()
	resp, _ := NewResponder(daemon, []byte("ag/v1|test|lab01"))
	if _, err := resp.ReadMessage1([]byte("garbage")); err == nil {
		t.Fatal("expected error for malformed msg1")
	}
}

func TestFullHandshakeRoundTrip(t *testing.T) {
	bridge, _ := GenerateStatic()
	daemon, _ := GenerateStatic()
	prologue := []byte("ag/v1|test|lab01")

	init, _ := NewInitiator(bridge, daemon.Pub(), prologue)
	msg1, _ := init.WriteMessage1()

	resp, _ := NewResponder(daemon, prologue)
	if _, err := resp.ReadMessage1(msg1); err != nil {
		t.Fatal(err)
	}
	msg2, daemonSess, err := resp.WriteMessage2()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg2) != ikMsg2EmptyPayloadLen {
		t.Fatalf("msg2 len = %d, want %d", len(msg2), ikMsg2EmptyPayloadLen)
	}
	bridgeSess, err := init.ReadMessage2(msg2)
	if err != nil {
		t.Fatal(err)
	}
	if bridgeSess == nil || daemonSess == nil {
		t.Fatal("both sessions must be non-nil after handshake")
	}
}

func TestPrologueMismatchFailsHandshake(t *testing.T) {
	bridge, _ := GenerateStatic()
	daemon, _ := GenerateStatic()

	init, _ := NewInitiator(bridge, daemon.Pub(), []byte("ag/v1|gw|lab01"))
	msg1, _ := init.WriteMessage1()

	// Responder uses a DIFFERENT prologue (gateway impersonation attempt).
	resp, _ := NewResponder(daemon, []byte("ag/v1|gw|lab02"))
	if _, err := resp.ReadMessage1(msg1); err == nil {
		t.Fatal("expected handshake to fail on prologue mismatch")
	}
}

func TestWrongDaemonPubFailsHandshake(t *testing.T) {
	bridge, _ := GenerateStatic()
	realDaemon, _ := GenerateStatic()
	wrongDaemon, _ := GenerateStatic()

	// Bridge thinks it is talking to wrongDaemon (pubkey mismatch).
	init, _ := NewInitiator(bridge, wrongDaemon.Pub(), []byte("ag/v1|gw|lab01"))
	msg1, _ := init.WriteMessage1()

	// Real daemon tries to read; its private key doesn't match wrongDaemon's pub.
	resp, _ := NewResponder(realDaemon, []byte("ag/v1|gw|lab01"))
	if _, err := resp.ReadMessage1(msg1); err == nil {
		t.Fatal("expected handshake to fail when bridge pinned wrong daemon pub")
	}
}

func TestResponderACLPattern(t *testing.T) {
	// This test documents the caller pattern: after ReadMessage1 returns
	// the peer's static pub, the caller checks it against an allowlist
	// BEFORE issuing WriteMessage2.
	allowed, _ := GenerateStatic()
	notAllowed, _ := GenerateStatic()
	daemon, _ := GenerateStatic()
	prologue := []byte("ag/v1|test|lab01")
	allowList := [][]byte{allowed.Pub()}

	check := func(peer []byte) bool {
		for _, a := range allowList {
			if bytes.Equal(a, peer) {
				return true
			}
		}
		return false
	}

	// Allowed bridge → check should pass.
	{
		init, _ := NewInitiator(allowed, daemon.Pub(), prologue)
		msg1, _ := init.WriteMessage1()
		resp, _ := NewResponder(daemon, prologue)
		peer, err := resp.ReadMessage1(msg1)
		if err != nil {
			t.Fatal(err)
		}
		if !check(peer) {
			t.Fatal("allowed peer rejected by check")
		}
	}
	// Not-allowed bridge → check should fail.
	{
		init, _ := NewInitiator(notAllowed, daemon.Pub(), prologue)
		msg1, _ := init.WriteMessage1()
		resp, _ := NewResponder(daemon, prologue)
		peer, _ := resp.ReadMessage1(msg1)
		if check(peer) {
			t.Fatal("non-allowed peer passed check")
		}
	}
}
