package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/state"
)

func newTestEnrollServerAt(t *testing.T, dir string) (*EnrollServer, *TokenStore) {
	t.Helper()
	tokens, _ := NewTokenStore(filepath.Join(dir, "tokens.json"))
	srv := &EnrollServer{
		Tokens:        tokens,
		StatePath:     filepath.Join(dir, "state.json"),
		GatewayURL:    "wss://gw.test/",
		CertPinSource: func() string { return "sha256:testpin" },
	}
	return srv, tokens
}

func newTestEnrollServer(t *testing.T) (*EnrollServer, *TokenStore) {
	t.Helper()
	return newTestEnrollServerAt(t, t.TempDir())
}

func seedState(t *testing.T, srv *EnrollServer, gs *state.GatewayState) {
	t.Helper()
	if err := state.SaveGateway(srv.StatePath, gs); err != nil {
		t.Fatal(err)
	}
}

func mustLoadState(t *testing.T, path string) *state.GatewayState {
	t.Helper()
	st, err := state.LoadGateway(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func postEnroll(t *testing.T, srv *EnrollServer, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	return w
}

// enrollDaemon issues a daemon token, enrolls it, and requires success.
func enrollDaemon(t *testing.T, tokens *TokenStore, srv *EnrollServer, name, pub string) {
	t.Helper()
	tok, err := tokens.Issue(TokenRequest{Role: "daemon", Name: name, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: pub})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("enroll %s: status %d body %s", name, w.Code, w.Body.String())
	}
}

func TestEnrollDaemonHappyPath(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	tok, _ := tokens.Issue(TokenRequest{
		Role:   "daemon",
		Name:   "lab01",
		Policy: &DaemonPolicy{DefaultWorkingDir: "/tmp/scratch", ForbiddenPorts: []int{22}},
		TTL:    time.Hour,
	})
	body, _ := json.Marshal(EnrollRequest{
		Token:           tok.Token,
		DevicePubkeyB64: "AAA=",
	})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp EnrollResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.APIKey == "" {
		t.Fatal("api_key not returned")
	}
	if resp.Name != "lab01" {
		t.Fatalf("name not returned: %q", resp.Name)
	}
	if resp.DefaultWorkingDir != "/tmp/scratch" {
		t.Fatalf("DefaultWorkingDir = %q, want /tmp/scratch", resp.DefaultWorkingDir)
	}
	if len(resp.ForbiddenPorts) != 1 || resp.ForbiddenPorts[0] != 22 {
		t.Fatalf("forbidden_ports lost: %+v", resp.ForbiddenPorts)
	}
	gs := mustLoadState(t, srv.StatePath)
	if len(gs.Daemons) != 1 || gs.Daemons[0].Name != "lab01" {
		t.Fatalf("daemon not registered: %+v", gs.Daemons)
	}
	if gs.Daemons[0].NoisePub != "AAA=" {
		t.Fatalf("noise pub not stored: %q", gs.Daemons[0].NoisePub)
	}
}

func TestEnrollBridgeRequiresDaemonRegistered(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	tok, _ := tokens.Issue(TokenRequest{Role: "bridge", Name: "laptop", Target: "lab01", TTL: time.Hour})
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "AAA="})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when target daemon not registered, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEnrollBridgeHappyAfterDaemon(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	seedState(t, srv, &state.GatewayState{
		Version: state.CurrentVersion,
		Daemons: []state.DaemonEntry{{Name: "lab01", DeviceID: "d", NoisePub: "pub01"}},
	})

	tok, _ := tokens.Issue(TokenRequest{Role: "bridge", Name: "laptop", Target: "lab01", TTL: time.Hour})
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "bridgePub"})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var resp EnrollResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.DaemonPubkey != "pub01" {
		t.Fatalf("daemon pubkey not propagated: %q", resp.DaemonPubkey)
	}
	if resp.TargetDaemon != "lab01" {
		t.Fatalf("target daemon not propagated: %q", resp.TargetDaemon)
	}
	gs := mustLoadState(t, srv.StatePath)
	if len(gs.Agents) != 1 || gs.Agents[0].Name != "laptop" {
		t.Fatalf("bridge not registered: %+v", gs.Agents)
	}
}

func TestEnrollBridgeMultiTarget(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	seedState(t, srv, &state.GatewayState{
		Version: state.CurrentVersion,
		Daemons: []state.DaemonEntry{
			{Name: "lab01", DeviceID: "d1", NoisePub: "pub01"},
			{Name: "lab02", DeviceID: "d2", NoisePub: "pub02"},
		},
	})
	tok, _ := tokens.Issue(TokenRequest{Role: "bridge", Name: "laptop", Targets: []string{"lab01", "lab02"}, TTL: time.Hour})
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "bridgePub"})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var resp EnrollResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Targets) != 2 {
		t.Fatalf("want 2 targets, got %+v", resp.Targets)
	}
	byName := map[string]string{}
	for _, tg := range resp.Targets {
		byName[tg.Name] = tg.DaemonPubkey
	}
	if byName["lab01"] != "pub01" || byName["lab02"] != "pub02" {
		t.Fatalf("target pubkeys wrong: %+v", resp.Targets)
	}
	// Legacy fields mirror the first target.
	if resp.TargetDaemon != resp.Targets[0].Name || resp.DaemonPubkey != resp.Targets[0].DaemonPubkey {
		t.Fatalf("legacy fields not first target: %q/%q", resp.TargetDaemon, resp.DaemonPubkey)
	}
	gs := mustLoadState(t, srv.StatePath)
	if len(gs.Agents) != 1 || len(gs.Agents[0].AllowedDaemons) != 2 {
		t.Fatalf("bridge AllowedDaemons not multi: %+v", gs.Agents)
	}
}

func TestEnrollBridgeMultiTargetMissingOneIs409(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	seedState(t, srv, &state.GatewayState{
		Version: state.CurrentVersion,
		Daemons: []state.DaemonEntry{{Name: "lab01", DeviceID: "d1", NoisePub: "pub01"}},
	})
	tok, _ := tokens.Issue(TokenRequest{Role: "bridge", Name: "laptop", Targets: []string{"lab01", "lab02"}, TTL: time.Hour})
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "bridgePub"})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when one target unenrolled, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEnrollRejectsBadToken(t *testing.T) {
	srv, _ := newTestEnrollServer(t)
	body, _ := json.Marshal(EnrollRequest{Token: "et_bogus", DevicePubkeyB64: "AAA="})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestEnrollRejectsGET(t *testing.T) {
	srv, _ := newTestEnrollServer(t)
	r := httptest.NewRequest("GET", "/v1/enroll", nil)
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestEnrollRejectsBadJSON(t *testing.T) {
	srv, _ := newTestEnrollServer(t)
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader([]byte("not-json")))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestEnrollDaemonUpsert(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	enrollDaemon(t, tokens, srv, "lab01", "pub1")
	enrollDaemon(t, tokens, srv, "lab01", "pub2")
	gs := mustLoadState(t, srv.StatePath)
	if len(gs.Daemons) != 1 {
		t.Fatalf("expected upsert (1 entry), got %d: %+v", len(gs.Daemons), gs.Daemons)
	}
	if gs.Daemons[0].NoisePub != "pub2" {
		t.Fatalf("pubkey not updated: %q", gs.Daemons[0].NoisePub)
	}
}

// TestEnrollDoesNotResurrectRevokedDaemon reproduces the revocation
// resurrection bug: the gateway's in-memory state used to be flushed
// wholesale after every enroll, so pruning a device from
// gateway-state.json (gateway-revoke) was undone by the next enroll.
// Enrollments must instead rebuild from the config baseline ⊕ the file
// as it exists on disk at write time.
func TestEnrollDoesNotResurrectRevokedDaemon(t *testing.T) {
	dir := t.TempDir()
	srv, tokens := newTestEnrollServerAt(t, dir)
	// Config baseline: pristine, carries no enrolled devices.
	srv.SetBase(&state.GatewayState{Version: state.CurrentVersion})

	enrollDaemon(t, tokens, srv, "old", "pub-old")

	// Simulate gateway-revoke: prune "old" from the state file only.
	// The gateway process keeps its (now stale) adopted snapshot.
	st := mustLoadState(t, srv.StatePath)
	kept := make([]state.DaemonEntry, 0, len(st.Daemons))
	for _, d := range st.Daemons {
		if d.Name != "old" {
			kept = append(kept, d)
		}
	}
	st.Daemons = kept
	if err := state.SaveGateway(srv.StatePath, st); err != nil {
		t.Fatal(err)
	}

	enrollDaemon(t, tokens, srv, "new", "pub-new")

	got := mustLoadState(t, srv.StatePath)
	for _, d := range got.Daemons {
		if d.Name == "old" {
			t.Fatalf("revoked daemon resurrected by later enroll: %+v", got.Daemons)
		}
	}
	if len(got.Daemons) != 1 || got.Daemons[0].Name != "new" {
		t.Fatalf("expected only new on disk, got %+v", got.Daemons)
	}
	// The adopted in-memory view must match the file.
	adopted := srv.CurrentState()
	for _, d := range adopted.Daemons {
		if d.Name == "old" {
			t.Fatalf("revoked daemon still in adopted state: %+v", adopted.Daemons)
		}
	}
}

// TestEnrollKeepsSiblingAdditions: two EnrollServer instances sharing
// one state path (the model for a second gateway process or the CLI)
// must not clobber each other's enrollments — each write merges with
// what is currently on disk.
func TestEnrollKeepsSiblingAdditions(t *testing.T) {
	dir := t.TempDir()
	srvA, tokensA := newTestEnrollServerAt(t, dir)
	srvB, tokensB := newTestEnrollServerAt(t, dir)

	enrollDaemon(t, tokensA, srvA, "d1", "pub1")
	enrollDaemon(t, tokensB, srvB, "d2", "pub2")

	got := mustLoadState(t, srvA.StatePath)
	names := map[string]bool{}
	for _, d := range got.Daemons {
		names[d.Name] = true
	}
	if !names["d1"] || !names["d2"] {
		t.Fatalf("sibling enrollment clobbered: %+v", got.Daemons)
	}
}

// TestEnrollDaemonReturnsDefaultWorkingDir issues a daemon token carrying
// a default working dir, enrolls, and asserts the gateway echoes it back
// verbatim (no more path-allowlist expansion machinery involved).
func TestEnrollDaemonReturnsDefaultWorkingDir(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	tok, err := tokens.Issue(TokenRequest{
		Role:   "daemon",
		Name:   "d1",
		Policy: &DaemonPolicy{DefaultWorkingDir: "/srv/app"},
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "AAA="})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp EnrollResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DefaultWorkingDir != "/srv/app" {
		t.Fatalf("DefaultWorkingDir = %q, want /srv/app", resp.DefaultWorkingDir)
	}
}
