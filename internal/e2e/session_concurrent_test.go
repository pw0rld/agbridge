package e2e

import (
	"fmt"
	"sync"
	"testing"
)

// TestSessionWireReorderDesyncs documents the protocol invariant
// underlying F4: noise CipherStates carry an implicit, monotonically
// increasing nonce, and the transport MUST deliver ciphertexts in the
// same order the sender consumed nonces. Out-of-order delivery
// permanently desyncs the receiver — after the first reorder its recv
// counter is one ahead/behind, and every subsequent frame fails MAC.
//
// The bug in F4 was that sendInner encrypted OUTSIDE the WSS write
// mutex, so two goroutines could consume nonces N and N+1 and then race
// to enqueue, landing on the wire in the reverse order. Once that
// happens once, the session is dead even though the TCP conn is still
// up: matches the observed "5/50 round 1 → 0/50 rounds 2-5".
//
// This test proves the invariant deterministically by encrypting in
// order on a single goroutine and then replaying in reverse — no
// scheduling dependence.
func TestSessionWireReorderDesyncs(t *testing.T) {
	snd, rcv := completeHandshake(t)
	ad := []byte("sid")

	const N = 5
	cts := make([][]byte, N)
	for i := 0; i < N; i++ {
		ct, err := snd.Encrypt(ad, []byte{byte(i)})
		if err != nil {
			t.Fatalf("encrypt %d: %v", i, err)
		}
		cts[i] = ct
	}

	ok := 0
	for i := N - 1; i >= 0; i-- {
		if _, err := rcv.Decrypt(ad, cts[i]); err == nil {
			ok++
		}
	}
	if ok > 1 {
		t.Fatalf("expected receiver to desync after first out-of-order frame; got %d/%d decrypted (the invariant is broken)", ok, N)
	}
}

// TestSessionConcurrentSendSafeDecryptsAll proves the fix pattern:
// Encrypt and the transport-enqueue under one mutex preserves nonce
// order on the wire, so the receiver decrypts every frame. This is the
// invariant daemonState.sendInner and router.sendInner now uphold via
// their dedicated sendMu.
//
// Without that mutex (the F4 bug) this test would either deadlock the
// race detector on flynn/noise/state.go's unsynchronized nonce counter,
// or — without -race — desync the receiver as in
// TestSessionWireReorderDesyncs.
func TestSessionConcurrentSendSafeDecryptsAll(t *testing.T) {
	snd, rcv := completeHandshake(t)
	ad := []byte("test-sid")

	var sendMu sync.Mutex
	var wire [][]byte
	sendSafe := func(plain []byte) {
		sendMu.Lock()
		defer sendMu.Unlock()
		ct, err := snd.Encrypt(ad, plain)
		if err != nil {
			t.Errorf("encrypt: %v", err)
			return
		}
		wire = append(wire, ct)
	}

	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sendSafe([]byte(fmt.Sprintf("msg-%d", i)))
		}(i)
	}
	wg.Wait()

	for i, ct := range wire {
		if _, err := rcv.Decrypt(ad, ct); err != nil {
			t.Fatalf("frame %d failed to decrypt under safe pattern: %v", i, err)
		}
	}
}
