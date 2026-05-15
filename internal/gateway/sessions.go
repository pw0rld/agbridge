package gateway

import (
	"fmt"
	"sync"
)

// Session is a registered, authenticated client (bridge or daemon) sitting
// behind a wss conn. SessionTable uses Role + Name + KeyHash to decide
// whether a session is still authorized after a CredRegistry.Replace; if not,
// Revoke calls Close on the conn.
type Session struct {
	Role    string // "bridge" or "daemon"
	Name    string
	KeyHash string // APIKeyHash for bridge, TokenHash for daemon
	Conn    connIO
}

// SessionTable tracks live sessions so SIGHUP-driven revocation can close
// the right conns without disturbing the rest.
type SessionTable struct {
	mu       sync.Mutex
	next     int
	sessions map[int]*Session
}

// NewSessionTable returns an empty table.
func NewSessionTable() *SessionTable {
	return &SessionTable{sessions: make(map[int]*Session)}
}

// Register adds s and returns its id plus a release function. Callers
// (handleBridge / handleDaemon) typically defer release.
func (st *SessionTable) Register(s *Session) (int, func()) {
	st.mu.Lock()
	id := st.next
	st.next++
	st.sessions[id] = s
	st.mu.Unlock()
	return id, func() {
		st.mu.Lock()
		delete(st.sessions, id)
		st.mu.Unlock()
	}
}

// Count returns the number of currently-registered sessions.
func (st *SessionTable) Count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.sessions)
}

// Revoke closes every session whose Role/Name/KeyHash no longer appears in
// creds. Returns the list of "role/name" pairs that were closed (useful for
// logging). Closing happens outside the table lock so a slow Close doesn't
// block other Register / Revoke calls.
func (st *SessionTable) Revoke(creds *CredRegistry) []string {
	st.mu.Lock()
	snapshot := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		snapshot = append(snapshot, s)
	}
	st.mu.Unlock()

	var closed []string
	for _, s := range snapshot {
		keep := false
		switch s.Role {
		case "bridge":
			if e, ok := creds.LookupAgent(s.Name); ok && e.APIKeyHash == s.KeyHash {
				keep = true
			}
		case "daemon":
			if e, ok := creds.LookupDaemon(s.Name); ok && e.TokenHash == s.KeyHash {
				keep = true
			}
		}
		if !keep {
			_ = s.Conn.Close()
			closed = append(closed, fmt.Sprintf("%s/%s", s.Role, s.Name))
		}
	}
	return closed
}
