// Package streamproto defines the JSON payload schemas for the stream
// multiplex frames used by the port_forward tool: StreamOpen, StreamAck,
// StreamData, StreamClose.
package streamproto

import "encoding/json"

// StreamOpen is the bridge → daemon connection request. Daemon dials
// RemoteHost:RemotePort on receipt.
type StreamOpen struct {
	StreamID   string `json:"stream_id"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
}

// StreamAck is the daemon → bridge dial result. Ok=true means dial
// succeeded and StreamData can flow; Ok=false means Err explains why.
type StreamAck struct {
	StreamID string `json:"stream_id"`
	Ok       bool   `json:"ok"`
	Err      string `json:"err,omitempty"`
}

// StreamData carries bytes in either direction.
type StreamData struct {
	StreamID string `json:"stream_id"`
	Data     []byte `json:"data"`
}

// StreamClose terminates the stream. Either side can send.
type StreamClose struct {
	StreamID string `json:"stream_id"`
	Err      string `json:"err,omitempty"`
}

func (s StreamOpen) Encode() ([]byte, error)  { return json.Marshal(s) }
func (s StreamAck) Encode() ([]byte, error)   { return json.Marshal(s) }
func (s StreamData) Encode() ([]byte, error)  { return json.Marshal(s) }
func (s StreamClose) Encode() ([]byte, error) { return json.Marshal(s) }

func DecodeStreamOpen(b []byte) (StreamOpen, error) {
	var s StreamOpen
	err := json.Unmarshal(b, &s)
	return s, err
}

func DecodeStreamAck(b []byte) (StreamAck, error) {
	var s StreamAck
	err := json.Unmarshal(b, &s)
	return s, err
}

func DecodeStreamData(b []byte) (StreamData, error) {
	var s StreamData
	err := json.Unmarshal(b, &s)
	return s, err
}

func DecodeStreamClose(b []byte) (StreamClose, error) {
	var s StreamClose
	err := json.Unmarshal(b, &s)
	return s, err
}
