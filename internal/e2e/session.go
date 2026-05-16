package e2e

import "github.com/flynn/noise"

// Session holds the post-handshake AEAD pair. Each side owns its own Session
// with send/recv cipher states pointing in opposite directions.
//
// Implicit nonce counters are maintained by the underlying CipherStates;
// neither side transmits the nonce on the wire. The transport layer (WSS)
// guarantees in-order delivery so counters stay in lockstep.
type Session struct {
	send *noise.CipherState
	recv *noise.CipherState
}

// Encrypt produces AEAD ciphertext for the outbound direction.
//
// ad is the Additional Authenticated Data — agbridge binds session_id ‖ reqid
// here so a ciphertext for one session/reqid cannot be replayed at another.
func (s *Session) Encrypt(ad, plaintext []byte) ([]byte, error) {
	return s.send.Encrypt(nil, ad, plaintext)
}

// Decrypt verifies and opens AEAD ciphertext for the inbound direction.
// Bad MAC, wrong nonce, or AD mismatch all return an error and DO NOT
// advance the receiving counter beyond the failed attempt.
func (s *Session) Decrypt(ad, ciphertext []byte) ([]byte, error) {
	return s.recv.Decrypt(nil, ad, ciphertext)
}
