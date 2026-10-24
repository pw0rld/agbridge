// Package fsproto defines payload schemas for the filesystem
// introspection tools: list_directory and stat. Both leak filesystem
// metadata in their responses.
package fsproto

import "encoding/json"

// DirEntry is the per-file metadata shared by ListDir and Stat responses.
type DirEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`     // "file" | "dir" | "symlink" | "other"
	Size    int64  `json:"size"`     // bytes; 0 for non-files
	Mode    uint32 `json:"mode"`     // Unix permission bits (low 9 bits useful)
	ModTime int64  `json:"mod_time"` // Unix seconds since epoch
}

// ListDirRequest asks for the immediate children of Path (not recursive).
type ListDirRequest struct {
	Path string `json:"path"`
}

// ListDirResponse carries all entries plus an optional error.
type ListDirResponse struct {
	Entries   []DirEntry `json:"entries"`
	Err       string     `json:"err,omitempty"`
	ErrDetail string     `json:"err_detail,omitempty"`
}

// StatRequest asks for metadata of a single path. Follow resolves the
// final path component if it is a symlink (like stat(1) -L); the default
// reports the link itself (lstat semantics).
type StatRequest struct {
	Path   string `json:"path"`
	Follow bool   `json:"follow,omitempty"`
}

// StatResponse: Entry nil iff Err is set.
type StatResponse struct {
	Entry     *DirEntry `json:"entry,omitempty"`
	Err       string    `json:"err,omitempty"`
	ErrDetail string    `json:"err_detail,omitempty"`
}

func (r ListDirRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
func (r ListDirResponse) Encode() ([]byte, error) { return json.Marshal(r) }
func (r StatRequest) Encode() ([]byte, error)     { return json.Marshal(r) }
func (r StatResponse) Encode() ([]byte, error)    { return json.Marshal(r) }

func DecodeListDirRequest(b []byte) (ListDirRequest, error) {
	var r ListDirRequest
	err := json.Unmarshal(b, &r)
	return r, err
}
func DecodeListDirResponse(b []byte) (ListDirResponse, error) {
	var r ListDirResponse
	err := json.Unmarshal(b, &r)
	return r, err
}
func DecodeStatRequest(b []byte) (StatRequest, error) {
	var r StatRequest
	err := json.Unmarshal(b, &r)
	return r, err
}
func DecodeStatResponse(b []byte) (StatResponse, error) {
	var r StatResponse
	err := json.Unmarshal(b, &r)
	return r, err
}
