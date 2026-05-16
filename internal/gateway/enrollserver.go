package gateway

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/pw0rld/agbridge/internal/auth"
	"github.com/pw0rld/agbridge/internal/config"
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

	// For bridges:
	TargetDaemon string `json:"target_daemon,omitempty"`
	DaemonPubkey string `json:"daemon_pubkey,omitempty"`

	// For daemons:
	AllowedExecCwds      []string `json:"allowed_exec_cwds,omitempty"`
	AllowedReadPaths     []string `json:"allowed_read_paths,omitempty"`
	AllowedWritePaths    []string `json:"allowed_write_paths,omitempty"`
	EnvAllowlist         []string `json:"env_allowlist,omitempty"`
	ForbiddenPorts       []int    `json:"forbidden_ports,omitempty"`
	AllowedBridgePubkeys []string `json:"allowed_bridge_pubkeys,omitempty"`
}

// EnrollServer is the HTTP handler bundling state + token store + cert pin.
// It owns no goroutines; mount HandleEnroll under /v1/enroll on the
// gateway's shared TLS listener.
type EnrollServer struct {
	Tokens        *TokenStore
	State         *state.GatewayState
	StatePath     string // persists gateway state after each successful enroll; "" disables
	GatewayURL    string // returned to device, e.g. wss://gw.example.com/
	CertPinSource func() string

	// Live, if non-nil, receives newly-enrolled agents/daemons so they
	// take effect without requiring SIGHUP.
	Live *CredRegistry

	mu sync.Mutex
}

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

	resp := EnrollResponse{
		DeviceID:   devID,
		Name:       tok.Name,
		APIKey:     apiKey,
		APIKeyHash: apiHash,
		GatewayURL: s.GatewayURL,
		CertPin:    s.CertPinSource(),
		E2EMode:    "required",
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch tok.Role {
	case "daemon":
		entry := state.DaemonEntry{
			Name:      tok.Name,
			DeviceID:  devID,
			TokenHash: apiHash,
			NoisePub:  req.DevicePubkeyB64,
		}
		replaced := false
		for i, d := range s.State.Daemons {
			if d.Name == tok.Name {
				s.State.Daemons[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			s.State.Daemons = append(s.State.Daemons, entry)
		}
		if tok.Policy != nil {
			paths := tok.Policy.AllowedPaths
			if len(tok.Policy.AllowedExecCwds) == 0 {
				resp.AllowedExecCwds = append([]string(nil), paths...)
			} else {
				resp.AllowedExecCwds = append([]string(nil), tok.Policy.AllowedExecCwds...)
			}
			if len(tok.Policy.AllowedReadPaths) == 0 {
				resp.AllowedReadPaths = append([]string(nil), paths...)
			} else {
				resp.AllowedReadPaths = append([]string(nil), tok.Policy.AllowedReadPaths...)
			}
			if len(tok.Policy.AllowedWritePaths) == 0 {
				resp.AllowedWritePaths = append([]string(nil), paths...)
			} else {
				resp.AllowedWritePaths = append([]string(nil), tok.Policy.AllowedWritePaths...)
			}
			resp.EnvAllowlist = append([]string(nil), tok.Policy.EnvAllowlist...)
			if len(resp.EnvAllowlist) == 0 {
				resp.EnvAllowlist = []string{"PATH", "HOME", "LANG"}
			}
			resp.ForbiddenPorts = append([]int(nil), tok.Policy.ForbiddenPorts...)
			resp.AllowedBridgePubkeys = append([]string(nil), tok.Policy.AllowedBridgePubkeys...)
		}

	case "bridge":
		var daemonPub string
		for _, d := range s.State.Daemons {
			if d.Name == tok.Target {
				daemonPub = d.NoisePub
				break
			}
		}
		if daemonPub == "" {
			http.Error(w, fmt.Sprintf("target daemon %q not yet enrolled", tok.Target), http.StatusConflict)
			return
		}
		entry := state.AgentEntry{
			Name:           tok.Name,
			DeviceID:       devID,
			APIKeyHash:     apiHash,
			AllowedDaemons: []string{tok.Target},
		}
		replaced := false
		for i, a := range s.State.Agents {
			if a.Name == tok.Name {
				s.State.Agents[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			s.State.Agents = append(s.State.Agents, entry)
		}
		resp.TargetDaemon = tok.Target
		resp.DaemonPubkey = daemonPub

	default:
		http.Error(w, "unknown role", http.StatusInternalServerError)
		return
	}

	if s.StatePath != "" {
		if err := state.SaveGateway(s.StatePath, s.State); err != nil {
			http.Error(w, "save state: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Push the new credential into the live CredRegistry so subsequent
	// WSS Hello frames find it without a SIGHUP cycle.
	if s.Live != nil {
		switch tok.Role {
		case "daemon":
			s.Live.AddDaemon(config.DaemonEntry{Name: tok.Name, TokenHash: apiHash})
		case "bridge":
			s.Live.AddAgent(config.AgentEntry{
				Name:           tok.Name,
				APIKeyHash:     apiHash,
				AllowedDaemons: []string{tok.Target},
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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
