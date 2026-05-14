// Package fileproto defines the JSON payload schemas for FileReadRequest,
// FileWriteRequest, FileChunk, FileComplete frames. Binary chunk data is
// base64-encoded on the wire (encoding/json's []byte handling).
package fileproto

import "encoding/json"

// FileReadRequest is the bridge → daemon read kickoff.
type FileReadRequest struct {
	Path    string `json:"path"`
	MaxSize int    `json:"max_size,omitempty"`
}

// FileWriteRequest is the bridge → daemon write kickoff. Subsequent FileChunk
// frames carry the actual bytes; last chunk MUST have Eof = true.
type FileWriteRequest struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode,omitempty"`
}

// FileChunk carries one slice of file content. Used for both read (daemon →
// bridge) and write (bridge → daemon).
type FileChunk struct {
	Data []byte `json:"data"`
	Eof  bool   `json:"eof,omitempty"`
}

// FileComplete is the terminator. Read: daemon → bridge. Write: daemon →
// bridge after Eof chunk processed. Err is empty on success.
type FileComplete struct {
	Size   int64  `json:"size"`
	Sha256 string `json:"sha256,omitempty"`
	Err    string `json:"err,omitempty"`
}

func (r FileReadRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
func (r FileWriteRequest) Encode() ([]byte, error) { return json.Marshal(r) }
func (c FileChunk) Encode() ([]byte, error)        { return json.Marshal(c) }
func (c FileComplete) Encode() ([]byte, error)     { return json.Marshal(c) }

func DecodeFileReadRequest(b []byte) (FileReadRequest, error) {
	var r FileReadRequest
	err := json.Unmarshal(b, &r)
	return r, err
}

func DecodeFileWriteRequest(b []byte) (FileWriteRequest, error) {
	var r FileWriteRequest
	err := json.Unmarshal(b, &r)
	return r, err
}

func DecodeFileChunk(b []byte) (FileChunk, error) {
	var c FileChunk
	err := json.Unmarshal(b, &c)
	return c, err
}

func DecodeFileComplete(b []byte) (FileComplete, error) {
	var c FileComplete
	err := json.Unmarshal(b, &c)
	return c, err
}
