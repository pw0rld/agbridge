package e2e

import (
	"errors"

	"github.com/flynn/noise"
)

// Initiator drives the Noise IK handshake from the bridge side.
// Bridge knows the daemon's static public key in advance.
type Initiator struct {
	hs *noise.HandshakeState
}

// NewInitiator constructs an IK initiator state.
//
//	local         bridge's long-term keypair
//	remoteStatic  daemon's long-term public key (32 bytes)
//	prologue      "agbridge/v1|<gateway-host>|<target-daemon>" — both sides MUST agree
func NewInitiator(local *StaticKey, remoteStatic []byte, prologue []byte) (*Initiator, error) {
	if local == nil {
		return nil, errors.New("e2e: nil local key")
	}
	if len(remoteStatic) != 32 {
		return nil, errors.New("e2e: remote static must be 32 bytes")
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   CipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		Prologue:      prologue,
		StaticKeypair: local.dh,
		PeerStatic:    remoteStatic,
	})
	if err != nil {
		return nil, err
	}
	return &Initiator{hs: hs}, nil
}

// WriteMessage1 produces the first handshake message (e, es, s, ss).
// For IK with an empty payload this is exactly 96 bytes:
//
//	32B ephemeral pub + 32B static pub + 16B static AEAD tag + 16B payload tag.
func (i *Initiator) WriteMessage1() ([]byte, error) {
	out, _, _, err := i.hs.WriteMessage(nil, nil)
	return out, err
}

// ReadMessage2 consumes the responder's reply (e, ee, se) and finalizes
// the handshake, returning the initiator's Session.
//
// After this call, cs1 (from Noise Split) is the initiator→responder cipher
// (this side's send), cs2 is the responder→initiator cipher (this side's recv).
func (i *Initiator) ReadMessage2(msg2 []byte) (*Session, error) {
	_, cs1, cs2, err := i.hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, err
	}
	if cs1 == nil || cs2 == nil {
		return nil, errors.New("e2e: handshake not finished after msg2")
	}
	return &Session{send: cs1, recv: cs2}, nil
}

// Responder drives the Noise IK handshake from the daemon side.
type Responder struct {
	hs   *noise.HandshakeState
	peer []byte // initiator's static pub, populated after ReadMessage1
}

// NewResponder constructs an IK responder state.
func NewResponder(local *StaticKey, prologue []byte) (*Responder, error) {
	if local == nil {
		return nil, errors.New("e2e: nil local key")
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   CipherSuite,
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		Prologue:      prologue,
		StaticKeypair: local.dh,
	})
	if err != nil {
		return nil, err
	}
	return &Responder{hs: hs}, nil
}

// ReadMessage1 consumes the initiator's first message and returns a copy
// of the initiator's static public key (revealed during IK msg1).
//
// Callers MUST check this against an allowlist before calling WriteMessage2.
func (r *Responder) ReadMessage1(msg1 []byte) ([]byte, error) {
	_, _, _, err := r.hs.ReadMessage(nil, msg1)
	if err != nil {
		return nil, err
	}
	pub := r.hs.PeerStatic()
	out := make([]byte, len(pub))
	copy(out, pub)
	r.peer = out
	cp := make([]byte, len(out))
	copy(cp, out)
	return cp, nil
}

// PeerStatic returns a copy of the initiator's static public key (only
// available after ReadMessage1).
func (r *Responder) PeerStatic() []byte {
	out := make([]byte, len(r.peer))
	copy(out, r.peer)
	return out
}

// WriteMessage2 produces the responder's reply (e, ee, se) and finalizes
// the handshake, returning the responder's Session.
//
// cs1 from Noise Split is initiator→responder (this side's recv); cs2 is
// responder→initiator (this side's send).
func (r *Responder) WriteMessage2() ([]byte, *Session, error) {
	out, cs1, cs2, err := r.hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, err
	}
	if cs1 == nil || cs2 == nil {
		return nil, nil, errors.New("e2e: handshake not finished after msg2")
	}
	return out, &Session{send: cs2, recv: cs1}, nil
}
