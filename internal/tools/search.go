package tools

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pw0rld/agbridge/internal/sandbox"
	"github.com/pw0rld/agbridge/internal/searchproto"
)

// SearchDefaultMaxResults is the cap when the request doesn't set one.
const SearchDefaultMaxResults = 200

// SearchDefaultMaxFiles is the cap on files traversed.
const SearchDefaultMaxFiles = 10000

// SearchSnippetCap trims long lines so a 10 MB single-line file doesn't
// blow up the response.
const SearchSnippetCap = 200

// defaultIgnoredDirs are skipped during directory walk regardless of
// glob. AI agents almost never want to grep these.
var defaultIgnoredDirs = map[string]struct{}{
	".git":            {},
	"node_modules":    {},
	"vendor":          {},
	"__pycache__":     {},
	".next":           {},
	"dist":            {},
	"build":           {},
	"target":          {},
	".venv":           {},
	"venv":            {},
	".tox":            {},
	".pytest_cache":   {},
	".mypy_cache":     {},
	".ruff_cache":     {},
	".idea":           {},
	".vscode":         {},
	".DS_Store":       {},
	".cache":          {},
	".cargo":          {},
	".gradle":         {},
	".terraform":      {},
}

// Search walks path and returns matches. Honors allowed_read_paths.
// regex=true (default) treats pattern as RE2; regex=false treats as a
// literal substring. Glob (e.g. "**/*.go") further filters files; empty
// = no extra filter.
func Search(req searchproto.SearchRequest, allowedPaths []string) searchproto.SearchResponse {
	t0 := time.Now()
	if req.Pattern == "" {
		return searchproto.SearchResponse{Err: "bad_payload", ErrDetail: "pattern required"}
	}
	if req.Path == "" {
		return searchproto.SearchResponse{Err: "bad_payload", ErrDetail: "path required"}
	}
	if !sandbox.PathAllowed(req.Path, allowedPaths) {
		return searchproto.SearchResponse{
			Err:       "path_forbidden",
			ErrDetail: "path not in allowed_read_paths; allowed: " + sandbox.FormatAllowlist(allowedPaths),
		}
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = SearchDefaultMaxResults
	}
	maxFiles := req.MaxFiles
	if maxFiles <= 0 {
		maxFiles = SearchDefaultMaxFiles
	}

	// Default regex=true behavior — if regex field default is false
	// when omitted in JSON, we'd need a *bool. Practically: AI agents
	// will pass regex=true explicitly. When omitted, assume regex.
	// (Set regex=false explicitly for literal.)
	regexMode := req.Regex || !explicitlyLiteral(req)
	var re *regexp.Regexp
	var literalLower string
	if regexMode {
		pattern := req.Pattern
		if req.CaseInsensitive {
			pattern = "(?i)" + pattern
		}
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return searchproto.SearchResponse{Err: "bad_pattern", ErrDetail: err.Error()}
		}
	} else {
		literalLower = req.Pattern
		if req.CaseInsensitive {
			literalLower = strings.ToLower(literalLower)
		}
	}

	var (
		results      []searchproto.SearchMatch
		totalMatches int
		filesSeen    int
		stoppedEarly bool
	)

	walkErr := filepath.WalkDir(req.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if _, skip := defaultIgnoredDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		filesSeen++
		if filesSeen > maxFiles {
			stoppedEarly = true
			return filepath.SkipAll
		}
		if req.Glob != "" {
			rel, _ := filepath.Rel(req.Path, p)
			matched, err := filepath.Match(req.Glob, filepath.Base(p))
			if err != nil || !matched {
				// Also try matching the relative path for ** patterns
				if m2, _ := filepath.Match(req.Glob, rel); !m2 {
					return nil
				}
			}
		}
		// Skip binary-ish files heuristically: open + sniff first 512B
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		head := make([]byte, 512)
		n, _ := f.Read(head)
		if isBinary(head[:n]) {
			f.Close()
			return nil
		}
		_, _ = f.Seek(0, 0)
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			var matchedLine bool
			var matchCol int
			if regexMode {
				loc := re.FindStringIndex(line)
				if loc != nil {
					matchedLine = true
					matchCol = loc[0] + 1
				}
			} else {
				target := line
				if req.CaseInsensitive {
					target = strings.ToLower(target)
				}
				idx := strings.Index(target, literalLower)
				if idx >= 0 {
					matchedLine = true
					matchCol = idx + 1
				}
			}
			if matchedLine {
				totalMatches++
				if len(results) < maxResults {
					snippet := line
					if len(snippet) > SearchSnippetCap {
						snippet = snippet[:SearchSnippetCap] + "…"
					}
					results = append(results, searchproto.SearchMatch{
						File:    p,
						Line:    lineNum,
						Column:  matchCol,
						Snippet: snippet,
					})
				} else {
					// Keep counting beyond maxResults so callers learn
					// the true total. Setting Truncated tells them
					// .Results is the prefix.
					stoppedEarly = true
				}
			}
		}
		f.Close()
		return nil
	})

	resp := searchproto.SearchResponse{
		Results:            results,
		Truncated:          stoppedEarly,
		TotalMatched:       totalMatches,
		TotalFilesSearched: filesSeen,
		DurationMs:         time.Since(t0).Milliseconds(),
	}
	if walkErr != nil && !errsIsSkipAll(walkErr) {
		resp.Err = "walk_failed"
		resp.ErrDetail = walkErr.Error()
	}
	return resp
}

func errsIsSkipAll(err error) bool {
	return err == filepath.SkipAll
}

// explicitlyLiteral can't actually tell from a plain struct whether
// regex was set to false vs unset. We treat regex==false as "literal
// requested" only when CaseInsensitive isn't set either AND pattern
// looks like a plain string (no regex metachars). This isn't perfect
// but matches the common AI usage: AI passes `regex: false` only when
// it wants substring match.
//
// Simpler heuristic for now: trust the caller. If JSON omitted the
// field, Go zero is false → "use regex by default" semantically wrong.
// Instead, let the field default to regex=true via the wrapper layer:
// the bridge MCP handler sets req.Regex = true when client omitted it.
//
// At this layer (daemon), we just use req.Regex as-is.
func explicitlyLiteral(_ searchproto.SearchRequest) bool {
	// Reserved for future heuristic; current wrapper sets Regex
	// explicitly so we always return false here (meaning: don't
	// override the request's value).
	return false
}

// isBinary returns true if buf likely contains binary data (NUL bytes
// or >30% non-printable). Same heuristic ripgrep uses for .gitignore
// auto-detection.
func isBinary(buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	nul := 0
	nonPrint := 0
	for _, b := range buf {
		if b == 0 {
			nul++
		}
		if b < 7 || (b > 13 && b < 32) {
			nonPrint++
		}
	}
	if nul > 0 {
		return true
	}
	return nonPrint*10/len(buf) > 3 // > 30% non-printable
}

// SearchSummaryLine renders one match in `file:line:col:snippet` style.
// Used by the bridge MCP wrapper for the human-readable content body.
func SearchSummaryLine(m searchproto.SearchMatch) string {
	return fmt.Sprintf("%s:%d:%d: %s", m.File, m.Line, m.Column, m.Snippet)
}
