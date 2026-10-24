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
	// maxReqIDLen must fit the 2-byte reqid_len wire field: 1<<16 would
	// overflow uint16 to 0 and emit a corrupted frame.
	maxReqIDLen   = 1<<16 - 1 // 65535
	maxPayloadLen = 1 << 26   // 64 MB
)

var (
	ErrReqIDTooLong    = errors.New("proto: reqid exceeds 65535 bytes")
	ErrPayloadTooLong  = errors.New("proto: payload exceeds 64 MB")
	ErrShortFrame      = errors.New("proto: frame truncated")
	ErrVersionMismatch = errors.New("proto: protocol version mismatch")
)

// FrameType identifies the kind of message carried in a Frame.
type FrameType uint8

const (
	FrameTypePing         FrameType = 1
	FrameTypePong         FrameType = 2
	FrameTypeError        FrameType = 3
	FrameTypeHello        FrameType = 10
	FrameTypeHelloAck     FrameType = 11
	FrameTypeRoute        FrameType = 12
	FrameTypeExecRequest  FrameType = 20
	FrameTypeExecChunk    FrameType = 21
	FrameTypeExecComplete FrameType = 22

	FrameTypeFileReadRequest  FrameType = 30
	FrameTypeFileWriteRequest FrameType = 31
	FrameTypeFileChunk        FrameType = 32
	FrameTypeFileComplete     FrameType = 33
	FrameTypeFileEditRequest  FrameType = 34
	FrameTypeListDirRequest   FrameType = 35
	FrameTypeListDirResponse  FrameType = 36
	FrameTypeStatRequest      FrameType = 37
	FrameTypeStatResponse     FrameType = 38

	FrameTypeStreamOpen  FrameType = 40
	FrameTypeStreamAck   FrameType = 41
	FrameTypeStreamData  FrameType = 42
	FrameTypeStreamClose FrameType = 43

	// E2E handshake (v0.1.0+). Gateway forwards as opaque; only peers parse.
	FrameTypeNoiseInit  FrameType = 50
	FrameTypeNoiseResp  FrameType = 51
	FrameTypeNoiseRekey FrameType = 52

	// Policy/capabilities introspection (v0.2.0+). Bridge → daemon empty
	// payload; daemon → bridge JSON describing current sandbox + meta.
	FrameTypeCapabilitiesRequest  FrameType = 60
	FrameTypeCapabilitiesResponse FrameType = 61

	// Bulk file transfer (v0.2.0+). Reuse FileChunk for both directions
	// and FileComplete for terminal status; new request types carry the
	// upload mode (chunks vs URL) and download offset/limit.
	FrameTypeUploadRequest   FrameType = 70
	FrameTypeDownloadRequest FrameType = 71

	// Background process management (v0.3.0+). A single request frame
	// carries action=start|status|logs|kill|list with action-specific
	// fields; the response frame mirrors the action plus result fields.
	FrameTypeBgExecRequest  FrameType = 80
	FrameTypeBgExecResponse FrameType = 81

	// Native search (v0.3.0+). Replaces "exec grep" so AI agents don't
	// have to deal with cross-platform grep / shell quoting / output
	// format inconsistencies.
	FrameTypeSearchRequest  FrameType = 90
	FrameTypeSearchResponse FrameType = 91
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

// Decode parses the wire format. Returns ErrVersionMismatch on protocol skew,
// ErrShortFrame on truncation, ErrPayloadTooLong on absurd payload_len.
func Decode(b []byte) (Frame, error) {
	if len(b) < 2 {
		return Frame{}, ErrShortFrame
	}
	if b[0] != ProtocolVersion {
		return Frame{}, ErrVersionMismatch
	}
	f := Frame{Type: FrameType(b[1])}
	off := 2
	if len(b) < off+2 {
		return Frame{}, ErrShortFrame
	}
	ridLen := int(binary.BigEndian.Uint16(b[off : off+2]))
	off += 2
	if len(b) < off+ridLen {
		return Frame{}, ErrShortFrame
	}
	f.ReqID = string(b[off : off+ridLen])
	off += ridLen
	if len(b) < off+4 {
		return Frame{}, ErrShortFrame
	}
	plLen := int(binary.BigEndian.Uint32(b[off : off+4]))
	off += 4
	if plLen > maxPayloadLen {
		return Frame{}, ErrPayloadTooLong
	}
	if len(b) < off+plLen {
		return Frame{}, ErrShortFrame
	}
	f.Payload = make([]byte, plLen)
	copy(f.Payload, b[off:off+plLen])
	return f, nil
}
