// Package searchproto defines the schema for the daemon-side search
// tool. Replaces "exec grep -rn ..." with a typed result list and a
// consistent ignore-set across platforms.
package searchproto

import "encoding/json"

// SearchRequest is the bridge → daemon search kickoff.
type SearchRequest struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Regex           bool   `json:"regex,omitempty"`             // default true; false = literal match
	Glob            string `json:"glob,omitempty"`              // optional **/*.go style filter
	CaseInsensitive bool   `json:"case_insensitive,omitempty"`
	MaxResults      int    `json:"max_results,omitempty"`       // default 200
	MaxFiles        int    `json:"max_files,omitempty"`         // default 10000
}

// SearchMatch is one hit.
type SearchMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`    // 1-indexed
	Column  int    `json:"column"`  // 1-indexed byte column
	Snippet string `json:"snippet"` // the full matching line, trimmed at 200 chars
}

// SearchResponse carries hits + meta.
type SearchResponse struct {
	Results            []SearchMatch `json:"results"`
	Truncated          bool          `json:"truncated"`
	TotalMatched       int           `json:"total_matched"`
	TotalFilesSearched int           `json:"total_files_searched"`
	DurationMs         int64         `json:"duration_ms"`
	Err                string        `json:"err,omitempty"`
	ErrDetail          string        `json:"err_detail,omitempty"`
}

func (r SearchRequest) Encode() ([]byte, error)  { return json.Marshal(r) }
func (r SearchResponse) Encode() ([]byte, error) { return json.Marshal(r) }

func DecodeSearchRequest(b []byte) (SearchRequest, error) {
	var r SearchRequest
	err := json.Unmarshal(b, &r)
	return r, err
}

func DecodeSearchResponse(b []byte) (SearchResponse, error) {
	var r SearchResponse
	err := json.Unmarshal(b, &r)
	return r, err
}
