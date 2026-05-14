package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
)

// HMACSize is the length of the appended MAC (HMAC-SHA256 → 32 bytes).
const HMACSize = sha256.Size

var (
	ErrShortMAC = errors.New("auth: signed frame shorter than HMAC size")
	ErrBadMAC   = errors.New("auth: MAC verification failed")
)

// SignFrame appends an HMAC-SHA256 over frame to the frame bytes.
// Returned slice is independent of the input.
func SignFrame(key, frame []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(frame)
	mac := m.Sum(nil)
	out := make([]byte, 0, len(frame)+HMACSize)
	out = append(out, frame...)
	out = append(out, mac...)
	return out
}

// VerifyFrame splits signed into (frame, mac) and verifies the MAC under key.
// Returns the inner frame on success.
func VerifyFrame(key, signed []byte) ([]byte, error) {
	if len(signed) < HMACSize {
		return nil, ErrShortMAC
	}
	cut := len(signed) - HMACSize
	frame := signed[:cut]
	mac := signed[cut:]
	want := hmac.New(sha256.New, key)
	want.Write(frame)
	if subtle.ConstantTimeCompare(mac, want.Sum(nil)) != 1 {
		return nil, ErrBadMAC
	}
	return frame, nil
}
