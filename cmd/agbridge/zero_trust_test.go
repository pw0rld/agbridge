package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// These tests pin the daemon's zero-trust invariants. They cover the
// "gateway can't extend daemon's ACL" property — the load-bearing
// guarantee for any deployment where the gateway runs on
// infrastructure the daemon operator doesn't trust.
//
// The invariants:
//
//   I1. Daemon's ACL state (allowed_read_paths, allowed_write_paths,
//       allowed_exec_cwds, allowed_bridge_pubkeys, forbidden_ports)
//       loads at boot from local state.json. No frame type can mutate
//       it. Tested by: TestDaemonDispatchHasNoConfigMutationCase.
//
//   I2. The daemon refuses Noise handshakes from any bridge whose
//       pubkey is not in allowed_bridge_pubkeys (when e2e_mode=
//       required and the allowlist is non-empty). Tested by:
//       TestPeerAllowedRejectsUnknownPubkey.
//
//   I3. Defense in depth: even if startup validation slipped and we
//       reached runtime with e2e_mode=required + empty allowlist, the
//       peer check still rejects every connection. Tested by:
//       TestPeerAllowedRejectsAllWhenRequiredAndEmpty.

func TestPeerAllowedNilE2E(t *testing.T) {
	s := &daemonState{e2e: nil}
	if s.peerAllowed([]byte("anything")) {
		t.Errorf("nil e2e must reject all peers (invariant I2)")
	}
}

func TestPeerAllowedEmptyAllowlistOptional(t *testing.T) {
	// Optional mode + empty allowlist is a valid operator choice
	// ("encrypt my traffic but I haven't pinned any specific bridge").
	// In that case the daemon trusts gateway-side authentication.
	s := &daemonState{e2e: &daemonE2EDeps{mode: "optional", allowedPubs: nil}}
	if !s.peerAllowed([]byte("any-pub-32-bytes")) {
		t.Errorf("optional mode with empty allowlist must accept")
	}
}

func TestPeerAllowedRejectsAllWhenRequiredAndEmpty(t *testing.T) {
	// Defense in depth (invariant I3): startup validation rejects
	// this config at boot, but if some future code path lets a daemon
	// reach runtime with required+empty, peerAllowed must still
	// refuse every connection.
	s := &daemonState{e2e: &daemonE2EDeps{mode: "required", allowedPubs: nil}}
	if s.peerAllowed([]byte("any-pub-32-bytes")) {
		t.Errorf("required mode + empty allowlist must reject at runtime")
	}
}

func TestPeerAllowedRejectsUnknownPubkey(t *testing.T) {
	// Invariant I2: a bridge pubkey not in the allowlist is rejected
	// regardless of mode.
	known := []byte("known-pub-32-bytes-known-pub-32xxx")
	unknown := []byte("unknown-32-bytes-unknown-32xxxxxxx")
	s := &daemonState{e2e: &daemonE2EDeps{
		mode:        "required",
		allowedPubs: [][]byte{known},
	}}
	if !s.peerAllowed(known) {
		t.Errorf("known pubkey must be accepted")
	}
	if s.peerAllowed(unknown) {
		t.Errorf("unknown pubkey must be rejected (invariant I2)")
	}
}

func TestPeerAllowedRejectsNearMatchPubkey(t *testing.T) {
	// Guards against prefix/length-only comparisons that future
	// refactors might accidentally introduce. The check must be
	// byte-exact.
	known := []byte("known-pub-32-bytes-known-pub-32xxx")
	tampered := append([]byte(nil), known...)
	tampered[len(tampered)-1] ^= 0x01 // flip one bit
	s := &daemonState{e2e: &daemonE2EDeps{
		mode:        "required",
		allowedPubs: [][]byte{known},
	}}
	if s.peerAllowed(tampered) {
		t.Errorf("one-bit-different pubkey must be rejected")
	}
}

// TestDaemonDispatchHasNoConfigMutationCase is a tripwire for
// invariant I1. It statically scans the dispatch() function in
// daemon.go and fails if any switch case body contains an assignment
// to s.e2e.* or s.cfg.* (the cached ACL state). Will fail loudly if a
// future PR adds a case like FrameTypeConfigUpdate /
// FrameTypeAllowlistPush / FrameTypePolicy that mutates ACL from a
// gateway-controlled frame.
//
// We do static analysis (not runtime) because dispatch() spawns
// goroutines for most handlers; those goroutines outlive the test
// function and a runtime test cannot reliably observe their effects
// without elaborate mocking. The static check is both stronger
// (catches mutation in any case, not just the ones we exercise) and
// simpler (no mocks).
func TestDaemonDispatchHasNoConfigMutationCase(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "daemon.go", nil, 0)
	if err != nil {
		t.Fatalf("parse daemon.go: %v", err)
	}

	var dispatch *ast.FuncDecl
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "dispatch" && fn.Recv != nil {
			dispatch = fn
			break
		}
	}
	if dispatch == nil {
		t.Fatal("could not find dispatch method in daemon.go")
	}

	// Walk dispatch body. Any assignment whose LHS is a selector
	// chain rooted at s.e2e or s.cfg is forbidden (would let a
	// gateway-driven frame rewrite the cached ACL).
	var violations []string
	ast.Inspect(dispatch.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			path := selectorChain(lhs)
			if path == "" {
				continue
			}
			if strings.HasPrefix(path, "s.e2e.") || strings.HasPrefix(path, "s.cfg.") {
				violations = append(violations, path)
			}
		}
		return true
	})

	if len(violations) > 0 {
		t.Errorf("dispatch() contains forbidden ACL-state mutations: %v\n"+
			"This breaks invariant I1 (daemon owns its ACL). See THREAT-MODEL.md.",
			violations)
	}
}

// selectorChain returns "s.e2e.allowedPubs" for an AST node like
// SelectorExpr{X:SelectorExpr{X:Ident("s"),Sel:"e2e"},Sel:"allowedPubs"},
// or "" if the node is not a pure selector chain rooted at an Ident.
func selectorChain(n ast.Node) string {
	switch v := n.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		left := selectorChain(v.X)
		if left == "" {
			return ""
		}
		return left + "." + v.Sel.Name
	default:
		return ""
	}
}
