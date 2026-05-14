// Package handshake encodes the initial Hello / HelloAck exchange that
// authenticates and identifies a peer before any application frames flow.
package handshake

import (
	"encoding/binary"
	"errors"
)

// Hello is the first message sent by bridge or daemon over a freshly
// dialed WSS connection.
type Hello struct {
	Role         string // "bridge" | "daemon"
	Name         string
	Secret       string // API key (bridge) or registration token (daemon)
	TargetDaemon string // bridge only; daemon leaves empty
}

var ErrHelloMalformed = errors.New("handshake: hello payload malformed")

const (
	maxStrU8  = 1 << 8
	maxStrU16 = 1 << 16
)

// Encode serializes Hello into:
//
//	role_len:1B | role:N | name_len:1B | name:N | secret_len:2B | secret:N | target_len:1B | target:N
func (h Hello) Encode() ([]byte, error) {
	if len(h.Role) >= maxStrU8 || len(h.Name) >= maxStrU8 || len(h.TargetDaemon) >= maxStrU8 {
		return nil, errors.New("handshake: role/name/target too long for 1-byte length prefix")
	}
	if len(h.Secret) >= maxStrU16 {
		return nil, errors.New("handshake: secret too long for 2-byte length prefix")
	}
	buf := make([]byte, 0, 1+len(h.Role)+1+len(h.Name)+2+len(h.Secret)+1+len(h.TargetDaemon))
	buf = append(buf, byte(len(h.Role)))
	buf = append(buf, h.Role...)
	buf = append(buf, byte(len(h.Name)))
	buf = append(buf, h.Name...)
	sl := make([]byte, 2)
	binary.BigEndian.PutUint16(sl, uint16(len(h.Secret)))
	buf = append(buf, sl...)
	buf = append(buf, h.Secret...)
	buf = append(buf, byte(len(h.TargetDaemon)))
	buf = append(buf, h.TargetDaemon...)
	return buf, nil
}

// DecodeHello parses a Hello payload.
func DecodeHello(b []byte) (Hello, error) {
	var h Hello
	off := 0
	read1 := func() (string, error) {
		if off >= len(b) {
			return "", ErrHelloMalformed
		}
		n := int(b[off])
		off++
		if off+n > len(b) {
			return "", ErrHelloMalformed
		}
		s := string(b[off : off+n])
		off += n
		return s, nil
	}
	var err error
	if h.Role, err = read1(); err != nil {
		return Hello{}, err
	}
	if h.Name, err = read1(); err != nil {
		return Hello{}, err
	}
	if off+2 > len(b) {
		return Hello{}, ErrHelloMalformed
	}
	sl := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if off+sl > len(b) {
		return Hello{}, ErrHelloMalformed
	}
	h.Secret = string(b[off : off+sl])
	off += sl
	if h.TargetDaemon, err = read1(); err != nil {
		return Hello{}, err
	}
	return h, nil
}
