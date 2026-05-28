package main

import (
	"encoding/json"
	"io"
)

// schemaVersion is bumped when the --json output of any command
// changes shape in a non-backwards-compatible way.
const schemaVersion = 1

// printJSON serialises payload with a top-level _schema field so AI
// callers can detect breaking changes without sniffing the binary
// version.
func printJSON(w io.Writer, payload map[string]any) {
	payload["_schema"] = schemaVersion
	_ = json.NewEncoder(w).Encode(payload)
}

// printErrorJSON writes the single-line error JSON used by every
// CLI subcommand on exit 1. Always goes to stderr, regardless of
// --json on stdout.
func printErrorJSON(w io.Writer, code, message string, retrySafe bool) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":       code,
		"message":    message,
		"retry_safe": retrySafe,
	})
}
