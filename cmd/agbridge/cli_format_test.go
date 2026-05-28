package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintJSONIncludesSchemaVersion(t *testing.T) {
	var out bytes.Buffer
	printJSON(&out, map[string]any{"name": "dev1"})
	got := out.String()
	if !strings.Contains(got, `"_schema":1`) {
		t.Errorf("expected _schema in JSON output, got: %s", got)
	}
	if !strings.Contains(got, `"name":"dev1"`) {
		t.Errorf("expected payload in JSON output, got: %s", got)
	}
}

func TestPrintErrorJSONOnStderr(t *testing.T) {
	var stderr bytes.Buffer
	printErrorJSON(&stderr, "not_found", "/foo missing", false)
	got := stderr.String()
	for _, want := range []string{
		`"code":"not_found"`,
		`"message":"/foo missing"`,
		`"retry_safe":false`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing field %q in: %s", want, got)
		}
	}
}
