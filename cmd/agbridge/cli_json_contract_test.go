package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// In --json mode a failed command must leave exactly one JSON object
// on stdout (the generic envelope, or the command's own envelope —
// never both) and no cobra usage dump anywhere.
func TestJSONErrorEnvelopeExactlyOneObject(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cliMain([]string{"config", "use", "ghost", "--state-dir", t.TempDir(), "--json"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected failure exit code 1, got %d (stdout: %s stderr: %s)", code, out.String(), errOut.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout must hold exactly one JSON object: %v (%q)", err, out.String())
	}
	if ok, _ := obj["ok"].(bool); ok {
		t.Fatalf("error envelope must carry ok=false: %+v", obj)
	}
	if e, _ := obj["error"].(string); e == "" {
		t.Fatalf("error envelope missing error message: %+v", obj)
	}
	if strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("usage dump leaked into stderr: %s", errOut.String())
	}
}

// enroll --json reports failures through its own richer envelope; the
// generic envelope must not be appended on top of it.
func TestEnrollJSONFailureNoDoubleEnvelope(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cliMain([]string{"enroll", "--json", "--state-dir", t.TempDir(),
		"--gateway", "://", "--token", "et_bogus"}, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected failure exit code 1, got %d (stdout: %s stderr: %s)", code, out.String(), errOut.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("stdout must hold exactly one JSON object: %v (%q)", err, out.String())
	}
	if _, ok := obj["steps"]; !ok {
		t.Fatalf("expected enroll outcome envelope, got %+v", obj)
	}
}

// Human mode: one plain line on stderr, stdout untouched, no usage.
func TestPlainErrorSingleLineNoUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	code := cliMain([]string{"config", "use", "ghost", "--state-dir", t.TempDir()}, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected failure exit code 1, got %d", code)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout must stay empty in human mode: %q", out.String())
	}
	s := errOut.String()
	if strings.Contains(s, "Usage:") {
		t.Fatalf("usage dump leaked: %s", s)
	}
	if n := strings.Count(strings.TrimRight(s, "\n"), "\n"); n != 0 {
		t.Fatalf("expected exactly one error line, got %d: %s", n+1, s)
	}
	if strings.Contains(s, "Error: ") {
		t.Fatalf("cobra must not double-report: %s", s)
	}
}
