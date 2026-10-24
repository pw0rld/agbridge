package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sync"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
	"github.com/pw0rld/agbridge/internal/filelock"
	"github.com/pw0rld/agbridge/internal/state"
)

// EnrollRequest is the device-side payload posted to /v1/enroll.
type EnrollRequest struct {
	Token           string `json:"token"`
	DevicePubkeyB64 string `json:"device_pubkey_b64"` // X25519 Noise static pub
}

// EnrollResponse is what the gateway returns on success.
type EnrollResponse struct {
	DeviceID   string `json:"device_id"`
	Name       string `json:"name"`
	APIKey     string `json:"api_key"`
	APIKeyHash string `json:"api_key_hash"`
	GatewayURL string `json:"gateway_url"`
	CertPin    string `json:"cert_pin"`
	E2EMode    string `json:"e2e_mode"`

	// For bridges. TargetDaemon/DaemonPubkey carry the first target for
	// backward compatibility with pre-multi-target clients; Targets
	// carries every authorized daemon and its Noise pubkey.
	TargetDaemon string         `json:"target_daemon,omitempty"`
	DaemonPubkey string         `json:"daemon_pubkey,omitempty"`
	Targets      []EnrollTarget `json:"targets,omitempty"`

	// For daemons:
	ExecEnabled          bool     `json:"exec_enabled,omitempty"`
	EnvAllowlist         []string `json:"env_allowlist,omitempty"`
	ForbiddenPorts       []int    `json:"forbidden_ports,omitempty"`
	AllowedBridgePubkeys []string `json:"allowed_bridge_pubkeys,omitempty"`
	DefaultWorkingDir    string   `json:"default_working_dir,omitempty"`
}

// EnrollTarget binds one authorized daemon's name to its Noise pubkey,
// so a multi-target bridge learns every target it may reach at enroll.
type EnrollTarget struct {
	Name         string `json:"name"`
	DaemonPubkey string `json:"daemon_pubkey"`
}

// EnrollServer is the HTTP handler bundling state + token store + cert pin.
// It owns no goroutines; mount HandleEnroll under /v1/enroll on the
// gateway's shared TLS listener.
type EnrollServer struct {
	Tokens        *TokenStore
	StatePath     string // persists gateway state after each successful enroll; "" disables
	GatewayURL    string // returned to device, e.g. wss://gw.example.com/
	CertPinSource func() string

	mu sync.Mutex
	// base is the config-derived state (gateway.yaml). Never mutated.
	// Each enrollment rebuilds the working state from base ⊕ the file on
	// disk, so base must never absorb enrolled-only entries: a sibling
	// CLI's gateway-revoke prune would otherwise be flushed back out by
	// the next enrollment (revocation resurrection).
	base *state.GatewayState
	// state is the snapshot adopted after the last successful
	// reload→mutate→save. Authoritative only when StatePath == ""
	// (nothing to reload from).
	state *state.GatewayState
	// live receives newly-enrolled agents/daemons so they take effect
	// without a SIGHUP. Guarded by mu: it is wired after the listener
	// starts serving, and refreshed on reload.
	live *CredRegistry
}

// SetBase installs the config-derived state baseline. Safe to call while
// serving (from the SIGHUP reload path after a config change).
func (s *EnrollServer) SetBase(base *state.GatewayState) {
	s.mu.Lock()
	s.base = base
	s.mu.Unlock()
}

// SetLive wires the running CredRegistry so enrollments authenticate
// immediately. Safe to call after the listener has started serving.
func (s *EnrollServer) SetLive(c *CredRegistry) {
	s.mu.Lock()
	s.live = c
	s.mu.Unlock()
}

// CurrentLive returns the wired CredRegistry, if any.
func (s *EnrollServer) CurrentLive() *CredRegistry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// CurrentState returns the snapshot adopted after the last enrollment.
func (s *EnrollServer) CurrentState() *state.GatewayState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != nil {
		return s.state
	}
	if s.base != nil {
		return s.base
	}
	return &state.GatewayState{Version: state.CurrentVersion}
}

// enrollUserError carries an HTTP status out of a mutateState callback.
type enrollUserError struct {
	status int
	msg    string
}

func (e *enrollUserError) Error() string { return e.msg }

// HandleEnroll is the http.HandlerFunc registered at POST /v1/enroll.
func (s *EnrollServer) HandleEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Token == "" || req.DevicePubkeyB64 == "" {
		http.Error(w, "token and device_pubkey_b64 required", http.StatusBadRequest)
		return
	}
	// Refuse names the config baseline already credentials: the baseline
	// always wins over the persisted file, so the enrollment would be
	// silently reverted by the next unrelated enrollment and the device
	// would be locked out with no audit trail. Peek (not Consume) first
	// so a rejected request does not burn the one-shot token.
	if peek, perr := s.Tokens.Peek(req.Token); perr == nil && s.baselineCredentials(peek.Role, peek.Name) {
		http.Error(w, fmt.Sprintf("%s %q is credentialed in the gateway config; edit the config instead of enrolling it", peek.Role, peek.Name), http.StatusConflict)
		return
	}
	tok, err := s.Tokens.Consume(req.Token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	apiKey, err := randomBase64(32)
	if err != nil {
		http.Error(w, "rand: "+err.Error(), http.StatusInternalServerError)
		return
	}
	apiHash := "sha256:" + auth.SHA256Hex([]byte(apiKey))
	devID := "d_" + randHex(16)

	// Default e2e_mode:
	//   bridge → required  (bridge has a specific target daemon pinned;
	//                       gateway returns daemon pub, so handshake works)
	//   daemon → optional  (no bridge pubkey known yet at first enroll;
	//                       operator opts into "required" via Policy.Strict)
	defaultE2E := "required"
	if tok.Role == "daemon" && (tok.Policy == nil || !tok.Policy.Strict) {
		defaultE2E = "optional"
	}
	resp := EnrollResponse{
		DeviceID:   devID,
		Name:       tok.Name,
		APIKey:     apiKey,
		APIKeyHash: apiHash,
		GatewayURL: s.GatewayURL,
		CertPin:    s.CertPinSource(),
		E2EMode:    defaultE2E,
	}

	if err := s.mutateState(func(st *state.GatewayState) error {
		switch tok.Role {
		case "daemon":
			entry := state.DaemonEntry{
				Name:      tok.Name,
				DeviceID:  devID,
				TokenHash: apiHash,
				NoisePub:  req.DevicePubkeyB64,
			}
			replaced := false
			for i, d := range st.Daemons {
				if d.Name == tok.Name {
					st.Daemons[i] = entry
					replaced = true
					break
				}
			}
			if !replaced {
				st.Daemons = append(st.Daemons, entry)
			}
			if tok.Policy != nil {
				resp.ExecEnabled = tok.Policy.ExecEnabled
				resp.EnvAllowlist = append([]string(nil), tok.Policy.EnvAllowlist...)
				if len(resp.EnvAllowlist) == 0 {
					resp.EnvAllowlist = []string{"PATH", "HOME", "LANG"}
				}
				resp.ForbiddenPorts = append([]int(nil), tok.Policy.ForbiddenPorts...)
				resp.AllowedBridgePubkeys = append([]string(nil), tok.Policy.AllowedBridgePubkeys...)
				resp.DefaultWorkingDir = tok.Policy.DefaultWorkingDir
			}

		case "bridge":
			// Resolve every authorized target's Noise pubkey. Any target that
			// isn't enrolled yet is a 409 — the bridge can't pin a key it
			// can't learn.
			for _, name := range tok.Targets {
				var daemonPub string
				for _, d := range st.Daemons {
					if d.Name == name {
						daemonPub = d.NoisePub
						break
					}
				}
				if daemonPub == "" {
					return &enrollUserError{http.StatusConflict,
						fmt.Sprintf("target daemon %q not yet enrolled", name)}
				}
				resp.Targets = append(resp.Targets, EnrollTarget{Name: name, DaemonPubkey: daemonPub})
			}
			entry := state.AgentEntry{
				Name:           tok.Name,
				DeviceID:       devID,
				APIKeyHash:     apiHash,
				AllowedDaemons: append([]string(nil), tok.Targets...),
			}
			replaced := false
			for i, a := range st.Agents {
				if a.Name == tok.Name {
					st.Agents[i] = entry
					replaced = true
					break
				}
			}
			if !replaced {
				st.Agents = append(st.Agents, entry)
			}
			resp.TargetDaemon = resp.Targets[0].Name
			resp.DaemonPubkey = resp.Targets[0].DaemonPubkey

		default:
			return &enrollUserError{http.StatusInternalServerError, "unknown role"}
		}
		return nil
	}); err != nil {
		var ue *enrollUserError
		if errors.As(err, &ue) {
			http.Error(w, ue.msg, ue.status)
		} else {
			http.Error(w, "save state: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Push the new credential into the live CredRegistry so subsequent
	// WSS Hello frames find it without a SIGHUP cycle.
	if live := s.CurrentLive(); live != nil {
		switch tok.Role {
		case "daemon":
			live.AddDaemon(config.DaemonEntry{Name: tok.Name, TokenHash: apiHash})
		case "bridge":
			live.AddAgent(config.AgentEntry{
				Name:           tok.Name,
				APIKeyHash:     apiHash,
				AllowedDaemons: append([]string(nil), tok.Targets...),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// mutateState rebuilds the working state under the in-process mutex and
// a cross-process file lock, applies fn, then persists before adopting.
// The in-memory snapshot is never flushed verbatim — the working state
// is always config base ⊕ what is currently on disk — so entries a
// sibling process pruned (gateway-revoke) stay pruned, and entries a
// sibling added are not clobbered. Holding the file lock across the
// save closes the window where a revoke could land between our reload
// and write.
func (s *EnrollServer) mutateState(fn func(st *state.GatewayState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.StatePath != "" {
		lock, err := filelock.Acquire(s.StatePath, filelock.DefaultTimeout)
		if err != nil {
			return err
		}
		defer lock.Release()
	}

	st, err := s.effectiveStateLocked()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	if s.StatePath != "" {
		if err := state.SaveGateway(s.StatePath, st); err != nil {
			return err
		}
	}
	s.state = st
	return nil
}

// effectiveStateLocked returns a fresh copy of the config baseline with
// entries that exist only in the persisted file layered in. The
// baseline never learns about enrollments, so anything pruned from
// disk stays gone. With StatePath == "" there is no file to reload
// from, so the in-memory snapshot is the only source of truth —
// starting from the baseline there would make every enrollment forget
// the previous one (device #2 evicting device #1).
//
// A persisted file we cannot read is an error, never a fallback: saving
// the baseline over it would silently delete every enrolled device.
func (s *EnrollServer) effectiveStateLocked() (*state.GatewayState, error) {
	var base *state.GatewayState
	if s.StatePath == "" {
		base = s.state
	}
	if base == nil {
		base = s.base
	}
	if base == nil {
		base = &state.GatewayState{Version: state.CurrentVersion}
	}
	out := copyGatewayState(base)
	if s.StatePath != "" {
		prior, err := state.LoadGateway(s.StatePath)
		switch {
		case err == nil:
			mergeEnrolled(out, prior)
		case errors.Is(err, fs.ErrNotExist):
			// First enrollment against this state file: nothing to merge.
		default:
			return nil, fmt.Errorf("enroll: reload %s: %w", s.StatePath, err)
		}
	}
	return out, nil
}

func copyGatewayState(in *state.GatewayState) *state.GatewayState {
	out := *in
	if in.Agents != nil {
		out.Agents = append([]state.AgentEntry(nil), in.Agents...)
	}
	if in.Daemons != nil {
		out.Daemons = append([]state.DaemonEntry(nil), in.Daemons...)
	}
	return &out
}

// mergeEnrolled layers prior entries that into lacks, and fills in
// credentials the baseline only declares as placeholders (empty hash).
//
// A name that both sides carry a credential for stays owned by the
// baseline: config is declarative, and letting a persisted record win
// would make a config edit silently ineffective. HandleEnroll therefore
// refuses to enroll such a name up front, so the two sources never
// disagree in the first place.
func mergeEnrolled(into, prior *state.GatewayState) {
	agents := make(map[string]int, len(into.Agents))
	for i, a := range into.Agents {
		agents[a.Name] = i
	}
	for _, a := range prior.Agents {
		i, ok := agents[a.Name]
		if !ok {
			into.Agents = append(into.Agents, a)
			agents[a.Name] = len(into.Agents) - 1
			continue
		}
		if into.Agents[i].APIKeyHash == "" && a.APIKeyHash != "" {
			into.Agents[i] = a
		}
	}
	daemons := make(map[string]int, len(into.Daemons))
	for i, d := range into.Daemons {
		daemons[d.Name] = i
	}
	for _, d := range prior.Daemons {
		i, ok := daemons[d.Name]
		if !ok {
			into.Daemons = append(into.Daemons, d)
			daemons[d.Name] = len(into.Daemons) - 1
			continue
		}
		if into.Daemons[i].TokenHash == "" && d.TokenHash != "" {
			into.Daemons[i] = d
		}
	}
}

// baselineCredentials reports whether the config baseline declares
// role/name with a real credential hash. An empty hash is a placeholder
// (the operator reserved the name), and enrollment is allowed to fill
// it in — mergeEnrolled lets the persisted credential win in that case.
func (s *EnrollServer) baselineCredentials(role, name string) bool {
	s.mu.Lock()
	base := s.base
	s.mu.Unlock()
	if base == nil {
		return false
	}
	switch role {
	case "daemon":
		for _, d := range base.Daemons {
			if d.Name == name {
				return d.TokenHash != ""
			}
		}
	case "bridge":
		for _, a := range base.Agents {
			if a.Name == name {
				return a.APIKeyHash != ""
			}
		}
	}
	return false
}

func randomBase64(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
