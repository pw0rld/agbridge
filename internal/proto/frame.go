// Package proto defines the binary frame envelope exchanged between
// bridge / gateway / daemon over any transport.
package proto

import (
	"encoding/binary"
	"errors"
)

// ProtocolVersion is the current wire format version. Receivers MUST
// close the connection on mismatch.
const ProtocolVersion uint8 = 1

const (
	maxReqIDLen   = 1 << 16
	maxPayloadLen = 1 << 26 // 64 MB
)

var (
	ErrReqIDTooLong   = errors.New("proto: reqid exceeds 64 KB")
	ErrPayloadTooLong = errors.New("proto: payload exceeds 64 MB")
)

// FrameType identifies the kind of message carried in a Frame.
type FrameType uint8

const (
	FrameTypePing  FrameType = 1
	FrameTypePong  FrameType = 2
	FrameTypeError FrameType = 3
)

// Frame is the on-the-wire envelope. Payload encoding is type-specific
// and opaque to this package.
type Frame struct {
	Type    FrameType
	ReqID   string
	Payload []byte
}

// Encode serializes the frame into the wire format:
//
//	version:1B | type:1B | reqid_len:2B | reqid:N | payload_len:4B | payload:M
//
// All multi-byte integers are big-endian.
func (f Frame) Encode() ([]byte, error) {
	if len(f.ReqID) > maxReqIDLen {
		return nil, ErrReqIDTooLong
	}
	if len(f.Payload) > maxPayloadLen {
		return nil, ErrPayloadTooLong
	}
	buf := make([]byte, 0, 1+1+2+len(f.ReqID)+4+len(f.Payload))
	buf = append(buf, ProtocolVersion, byte(f.Type))
	rid := make([]byte, 2)
	binary.BigEndian.PutUint16(rid, uint16(len(f.ReqID)))
	buf = append(buf, rid...)
	buf = append(buf, f.ReqID...)
	pl := make([]byte, 4)
	binary.BigEndian.PutUint32(pl, uint32(len(f.Payload)))
	buf = append(buf, pl...)
	buf = append(buf, f.Payload...)
	return buf, nil
}
