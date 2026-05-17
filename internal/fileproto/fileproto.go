// Package fileproto defines the JSON payload schemas for FileReadRequest,
// FileWriteRequest, FileChunk, FileComplete frames. Binary chunk data is
// base64-encoded on the wire (encoding/json's []byte handling).
package fileproto

import "encoding/json"

// FileReadRequest is the bridge → daemon read kickoff. Offset and Limit
// enable AI-friendly partial reads of large files (e.g., view lines
// 100-110 of a 5 KLOC source without loading the whole thing into
// context). MaxSize caps the total returned bytes regardless of Limit.
type FileReadRequest struct {
	Path    string `json:"path"`
	MaxSize int    `json:"max_size,omitempty"`
	Offset  int64  `json:"offset,omitempty"` // byte offset from start of file (0 = head)
	Limit   int64  `json:"limit,omitempty"`  // max bytes to read (0 = read to EOF or MaxSize cap)
}

// FileWriteRequest is the bridge → daemon write kickoff. Subsequent FileChunk
// frames carry the actual bytes; last chunk MUST have Eof = true.
type FileWriteRequest struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode,omitempty"`
}

// FileEditRequest is the bridge → daemon partial-edit request. The daemon
// reads the file, replaces OldString with NewString (must be unique unless
// ReplaceAll is set), and atomically writes the result back. The whole
// exchange is one round-trip — far cheaper than read_file + write_file
// for AI agents making small changes to large files.
type FileEditRequest struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// FileChunk carries one slice of file content. Used for both read (daemon →
// bridge) and write (bridge → daemon).
type FileChunk struct {
	Data []byte `json:"data"`
	Eof  bool   `json:"eof,omitempty"`
}

// FileComplete is the terminator. Read: daemon → bridge. Write: daemon →
// bridge after Eof chunk processed. Err is empty on success. ErrDetail
// is an operator-/AI-readable description (e.g., allowlist contents on
// path_forbidden) — old bridges can ignore it.
type FileComplete struct {
	Size      int64  `json:"size"`
	Sha256    string `json:"sha256,omitempty"`
	Err       string `json:"err,omitempty"`
	ErrDetail string `json:"err_detail,omitempty"`
}

func (r FileReadRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
func (r FileWriteRequest) Encode() ([]byte, error) { return json.Marshal(r) }
func (r FileEditRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
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

func DecodeFileEditRequest(b []byte) (FileEditRequest, error) {
	var r FileEditRequest
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
