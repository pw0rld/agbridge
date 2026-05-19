package e2e

import "github.com/flynn/noise"

// Session holds the post-handshake AEAD pair. Each side owns its own Session
// with send/recv cipher states pointing in opposite directions.
//
// Implicit nonce counters are maintained by the underlying CipherStates;
// neither side transmits the nonce on the wire. The transport layer (WSS)
// guarantees in-order delivery so counters stay in lockstep.
//
// CONCURRENCY CONTRACT: Encrypt and Decrypt are NOT goroutine-safe. The
// underlying noise.CipherState has an unsynchronized internal nonce counter
// (-race flags it). Callers MUST serialize Encrypt with the subsequent
// transport-enqueue as one atomic critical section — otherwise concurrent
// goroutines can consume nonces N, N+1 and race to enqueue, landing on the
// wire reversed. After even one such reorder the receiver's monotone recv
// counter desyncs permanently and every subsequent frame fails MAC. See
// session_concurrent_test.go for the invariant and F4 for the production
// regression.
type Session struct {
	send *noise.CipherState
	recv *noise.CipherState
}

// Encrypt produces AEAD ciphertext for the outbound direction.
//
// ad is the Additional Authenticated Data — agbridge binds session_id ‖ reqid
// here so a ciphertext for one session/reqid cannot be replayed at another.
//
// Not concurrent-safe — see the Session concurrency contract.
func (s *Session) Encrypt(ad, plaintext []byte) ([]byte, error) {
	return s.send.Encrypt(nil, ad, plaintext)
}

// Decrypt verifies and opens AEAD ciphertext for the inbound direction.
// Bad MAC, wrong nonce, or AD mismatch all return an error and DO NOT
// advance the receiving counter beyond the failed attempt.
//
// Not concurrent-safe — see the Session concurrency contract. In practice
// receivers consume frames from a single-reader Recv loop so callers
// rarely need an explicit mutex here.
func (s *Session) Decrypt(ad, ciphertext []byte) ([]byte, error) {
	return s.recv.Decrypt(nil, ad, ciphertext)
}
