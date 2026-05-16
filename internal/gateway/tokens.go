package gateway

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Token is a one-shot enrollment credential. After Consume() it is
// permanently marked used (stored on disk).
type Token struct {
	Token     string        `json:"token"`
	Role      string        `json:"role"`             // "bridge" | "daemon"
	Name      string        `json:"name"`             // device name
	Target    string        `json:"target,omitempty"` // bridge only: target daemon
	Policy    *DaemonPolicy `json:"policy,omitempty"` // daemon only
	ExpiresAt time.Time     `json:"expires_at"`
	Used      bool          `json:"used"`
	UsedAt    time.Time     `json:"used_at,omitempty"`
}

// DaemonPolicy is pre-set by the gateway operator at issue-token time.
// Daemon enroll request cannot widen these (v0.2 may add shrinking).
type DaemonPolicy struct {
	AllowedExecCwds      []string `json:"allowed_exec_cwds,omitempty"`
	AllowedReadPaths     []string `json:"allowed_read_paths,omitempty"`
	AllowedWritePaths    []string `json:"allowed_write_paths,omitempty"`
	AllowedPaths         []string `json:"allowed_paths,omitempty"` // shorthand → applied to all three when specifics are empty
	EnvAllowlist         []string `json:"env_allowlist,omitempty"`
	ForbiddenPorts       []int    `json:"forbidden_ports,omitempty"`
	AllowedBridgePubkeys []string `json:"allowed_bridge_pubkeys,omitempty"`
	// Strict, when true, forces the daemon into e2e_mode=required and
	// rejects bridges whose pubkey isn't in AllowedBridgePubkeys.
	// Default (false) → e2e_mode=optional (Noise IK still negotiated,
	// but with an empty allowlist any bridge under the same tenant is
	// accepted — operator can later tighten via re-enroll).
	Strict bool `json:"strict,omitempty"`
}

// TokenRequest is the input to Issue().
type TokenRequest struct {
	Role   string
	Name   string
	Target string
	Policy *DaemonPolicy
	TTL    time.Duration
}

// TokenStore is the gateway-side enrollment-token registry.
// Backed by a single JSON file with atomic tmp+rename writes.
type TokenStore struct {
	path string
	mu   sync.Mutex
	toks map[string]*Token
}

// NewTokenStore opens or creates a token JSON file at path.
func NewTokenStore(path string) (*TokenStore, error) {
	s := &TokenStore{path: path, toks: map[string]*Token{}}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &s.toks); err != nil {
			return nil, fmt.Errorf("tokens: parse %s: %w", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// Issue creates and persists a new token.
func (s *TokenStore) Issue(req TokenRequest) (*Token, error) {
	if req.Role != "bridge" && req.Role != "daemon" {
		return nil, fmt.Errorf("tokens: role must be bridge or daemon, got %q", req.Role)
	}
	if req.Name == "" {
		return nil, errors.New("tokens: name required")
	}
	if req.Role == "bridge" && req.Target == "" {
		return nil, errors.New("tokens: target daemon required for bridge tokens")
	}
	if req.TTL <= 0 {
		req.TTL = 15 * time.Minute
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	tok := &Token{
		Token:     "et_" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw),
		Role:      req.Role,
		Name:      req.Name,
		Target:    req.Target,
		Policy:    req.Policy,
		ExpiresAt: time.Now().Add(req.TTL),
	}
	s.mu.Lock()
	// Pull in any tokens issued by a sibling process (e.g. CLI vs running
	// gateway). The single tokens.json file is the source of truth.
	_ = s.reloadLocked()
	s.toks[tok.Token] = tok
	err := s.flushLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return tok, nil
}

// Consume validates and marks a token used. Caller must call once.
// Subsequent calls with the same token return an error.
func (s *TokenStore) Consume(t string) (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Tokens may have been issued by a sibling process (the CLI
	// `agbridge issue-token`) after we loaded the store; refresh
	// from disk before lookup so we don't 401 a valid token.
	_ = s.reloadLocked()
	tok, ok := s.toks[t]
	if !ok {
		return nil, errors.New("tokens: not found")
	}
	if tok.Used {
		return nil, errors.New("tokens: already used")
	}
	if time.Now().After(tok.ExpiresAt) {
		return nil, errors.New("tokens: expired")
	}
	tok.Used = true
	tok.UsedAt = time.Now()
	if err := s.flushLocked(); err != nil {
		return nil, err
	}
	out := *tok
	return &out, nil
}

// reloadLocked re-reads tokens.json into s.toks. Callers must hold s.mu.
// Errors are non-fatal (the in-memory state is preserved).
func (s *TokenStore) reloadLocked() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	fresh := map[string]*Token{}
	if err := json.Unmarshal(b, &fresh); err != nil {
		return err
	}
	s.toks = fresh
	return nil
}

// GC removes used + expired tokens older than retention.
func (s *TokenStore) GC(retention time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-retention)
	for k, t := range s.toks {
		if t.Used && t.UsedAt.Before(cutoff) {
			delete(s.toks, k)
		}
		if !t.Used && t.ExpiresAt.Before(cutoff) {
			delete(s.toks, k)
		}
	}
	return s.flushLocked()
}

// List returns a copy of all known tokens (used for gateway list-devices).
func (s *TokenStore) List() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, 0, len(s.toks))
	for _, t := range s.toks {
		out = append(out, *t)
	}
	return out
}

func (s *TokenStore) flushLocked() error {
	dir := filepath.Dir(s.path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp, err := os.CreateTemp(dir, ".tokens-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.toks); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
