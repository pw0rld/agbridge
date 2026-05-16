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

func newTestEnrollServer(t *testing.T) (*EnrollServer, *TokenStore, *state.GatewayState) {
	t.Helper()
	dir := t.TempDir()
	tokens, _ := NewTokenStore(filepath.Join(dir, "tokens.json"))
	gs := &state.GatewayState{Version: 1, Listen: ":443", AuditPath: "/dev/null"}
	srv := &EnrollServer{
		Tokens:        tokens,
		State:         gs,
		StatePath:     filepath.Join(dir, "state.json"),
		GatewayURL:    "wss://gw.test/",
		CertPinSource: func() string { return "sha256:testpin" },
	}
	return srv, tokens, gs
}

func TestEnrollDaemonHappyPath(t *testing.T) {
	srv, tokens, gs := newTestEnrollServer(t)
	tok, _ := tokens.Issue(TokenRequest{
		Role:   "daemon",
		Name:   "lab01",
		Policy: &DaemonPolicy{AllowedPaths: []string{"/tmp/scratch"}, ForbiddenPorts: []int{22}},
		TTL:    time.Hour,
	})
	body, _ := json.Marshal(EnrollRequest{
		Token:           tok.Token,
		DevicePubkeyB64: "AAA=",
	})
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
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
	if len(resp.AllowedExecCwds) != 1 || resp.AllowedExecCwds[0] != "/tmp/scratch" {
		t.Fatalf("policy not piped through: %+v", resp)
	}
	if len(resp.ForbiddenPorts) != 1 || resp.ForbiddenPorts[0] != 22 {
		t.Fatalf("forbidden_ports lost: %+v", resp.ForbiddenPorts)
	}
	if len(gs.Daemons) != 1 || gs.Daemons[0].Name != "lab01" {
		t.Fatalf("daemon not registered: %+v", gs.Daemons)
	}
	if gs.Daemons[0].NoisePub != "AAA=" {
		t.Fatalf("noise pub not stored: %q", gs.Daemons[0].NoisePub)
	}
}

func TestEnrollBridgeRequiresDaemonRegistered(t *testing.T) {
	srv, tokens, _ := newTestEnrollServer(t)
	tok, _ := tokens.Issue(TokenRequest{Role: "bridge", Name: "laptop", Target: "lab01", TTL: time.Hour})
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "AAA="})
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 when target daemon not registered, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEnrollBridgeHappyAfterDaemon(t *testing.T) {
	srv, tokens, gs := newTestEnrollServer(t)
	gs.Daemons = []state.DaemonEntry{{Name: "lab01", DeviceID: "d", NoisePub: "pub01"}}

	tok, _ := tokens.Issue(TokenRequest{Role: "bridge", Name: "laptop", Target: "lab01", TTL: time.Hour})
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "bridgePub"})
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
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
	if len(gs.Agents) != 1 || gs.Agents[0].Name != "laptop" {
		t.Fatalf("bridge not registered: %+v", gs.Agents)
	}
}

func TestEnrollRejectsBadToken(t *testing.T) {
	srv, _, _ := newTestEnrollServer(t)
	body, _ := json.Marshal(EnrollRequest{Token: "et_bogus", DevicePubkeyB64: "AAA="})
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestEnrollRejectsGET(t *testing.T) {
	srv, _, _ := newTestEnrollServer(t)
	r := httptest.NewRequest("GET", "/v1/enroll", nil)
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestEnrollRejectsBadJSON(t *testing.T) {
	srv, _, _ := newTestEnrollServer(t)
	r := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader([]byte("not-json")))
	w := httptest.NewRecorder()
	srv.HandleEnroll(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestEnrollDaemonUpsert(t *testing.T) {
	srv, tokens, gs := newTestEnrollServer(t)
	// First enroll
	tok1, _ := tokens.Issue(TokenRequest{Role: "daemon", Name: "lab01", TTL: time.Hour})
	body1, _ := json.Marshal(EnrollRequest{Token: tok1.Token, DevicePubkeyB64: "pub1"})
	r1 := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body1))
	w1 := httptest.NewRecorder()
	srv.HandleEnroll(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first enroll: %d", w1.Code)
	}
	// Re-enroll same name
	tok2, _ := tokens.Issue(TokenRequest{Role: "daemon", Name: "lab01", TTL: time.Hour})
	body2, _ := json.Marshal(EnrollRequest{Token: tok2.Token, DevicePubkeyB64: "pub2"})
	r2 := httptest.NewRequest("POST", "/v1/enroll", bytes.NewReader(body2))
	w2 := httptest.NewRecorder()
	srv.HandleEnroll(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second enroll: %d", w2.Code)
	}
	if len(gs.Daemons) != 1 {
		t.Fatalf("expected upsert (1 entry), got %d", len(gs.Daemons))
	}
	if gs.Daemons[0].NoisePub != "pub2" {
		t.Fatalf("pubkey not updated: %q", gs.Daemons[0].NoisePub)
	}
}
