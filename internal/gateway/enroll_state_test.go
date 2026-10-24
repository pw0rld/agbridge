package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/pw0rld/agbridge/internal/state"
)

// TestEnrollRefusesConfigCredentialedName pins the 409 for a name the
// config baseline already credentials. Accepting it would write a
// credential the next unrelated enrollment silently reverts (the
// baseline always wins), locking the device out with no audit trail.
// The one-shot token must survive the rejection.
func TestEnrollRefusesConfigCredentialedName(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	srv.SetBase(&state.GatewayState{
		Version: state.CurrentVersion,
		Daemons: []state.DaemonEntry{
			{Name: "lab01", TokenHash: "sha256:from-config"},
		},
	})

	tok, err := tokens.Issue(TokenRequest{Role: "daemon", Name: "lab01", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "AAA="})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
	if _, err := tokens.Peek(tok.Token); err != nil {
		t.Fatalf("rejected enrollment burned the token: %v", err)
	}
	if _, err := state.LoadGateway(srv.StatePath); err == nil {
		t.Fatal("rejected enrollment wrote state anyway")
	}
}

// TestEnrollFillsConfigPlaceholder covers the complementary case: a
// config entry with no credential is a placeholder, enrollment fills
// it in, and a later unrelated enrollment must not wipe it back to the
// empty baseline value.
func TestEnrollFillsConfigPlaceholder(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	srv.SetBase(&state.GatewayState{
		Version: state.CurrentVersion,
		Daemons: []state.DaemonEntry{
			{Name: "lab01"}, // declared, not yet credentialed
		},
	})

	enrollDaemon(t, tokens, srv, "lab01", "pub1")
	enrollDaemon(t, tokens, srv, "lab02", "pub2") // unrelated enrollment

	got := mustLoadState(t, srv.StatePath)
	byName := map[string]state.DaemonEntry{}
	for _, d := range got.Daemons {
		byName[d.Name] = d
	}
	if byName["lab01"].TokenHash == "" {
		t.Fatalf("placeholder credential was wiped by a later enrollment: %+v", got.Daemons)
	}
	if byName["lab02"].TokenHash == "" {
		t.Fatalf("second device missing credential: %+v", got.Daemons)
	}
}

// TestEnrollWithoutStateFileKeepsPreviousDevice covers persistence
// being disabled (StatePath == ""): the in-memory snapshot is the only
// source of truth, so a second enrollment must not evict the first.
func TestEnrollWithoutStateFileKeepsPreviousDevice(t *testing.T) {
	tokens, err := NewTokenStore(t.TempDir() + "/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := &EnrollServer{
		Tokens:        tokens,
		StatePath:     "",
		GatewayURL:    "wss://gw.test/",
		CertPinSource: func() string { return "sha256:testpin" },
	}
	srv.SetBase(&state.GatewayState{Version: state.CurrentVersion})

	enrollDaemon(t, tokens, srv, "d1", "pub1")
	enrollDaemon(t, tokens, srv, "d2", "pub2")

	st := srv.CurrentState()
	names := map[string]bool{}
	for _, d := range st.Daemons {
		names[d.Name] = true
	}
	if !names["d1"] || !names["d2"] {
		t.Fatalf("in-memory enrollments lost a device: %+v", st.Daemons)
	}
}

// TestEnrollAbortsOnUnreadableStateFile pins the fail-closed reload:
// a state file we cannot parse must abort the enrollment (500) rather
// than be replaced by a baseline-only snapshot that deletes every
// enrolled device.
func TestEnrollAbortsOnUnreadableStateFile(t *testing.T) {
	srv, tokens := newTestEnrollServer(t)
	if err := os.WriteFile(srv.StatePath, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := tokens.Issue(TokenRequest{Role: "daemon", Name: "d1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(EnrollRequest{Token: tok.Token, DevicePubkeyB64: "AAA="})
	w := postEnroll(t, srv, body)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(srv.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{corrupt" {
		t.Fatalf("unreadable state file was overwritten: %q", got)
	}
}
