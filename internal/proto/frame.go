// Package proto defines the binary frame envelope exchanged between
// bridge / gateway / daemon over any transport.
package proto

// ProtocolVersion is the current wire format version. Receivers MUST
// close the connection on mismatch.
const ProtocolVersion uint8 = 1

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
