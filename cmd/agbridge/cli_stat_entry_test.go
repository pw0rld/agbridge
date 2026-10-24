package main

import (
	"encoding/json"
	"testing"

	"github.com/pw0rld/agbridge/internal/fsproto"
)

// statHandlerResult is what StatHandler puts in the _meta envelope on
// the classic one-shot path: a typed pointer.
func statHandlerResult() map[string]any {
	return map[string]any{
		"_meta": map[string]any{
			"entry": &fsproto.DirEntry{
				Name:    "notes.txt",
				Type:    "file",
				Size:    42,
				Mode:    0o644,
				ModTime: 1700000000,
			},
		},
	}
}

func TestExtractStatEntryClassicShape(t *testing.T) {
	got, ok := extractStatEntry(statHandlerResult())
	if !ok {
		t.Fatal("extractStatEntry rejected the typed shape")
	}
	if got.Name != "notes.txt" || got.Size != 42 || got.Mode != 0o644 {
		t.Fatalf("entry = %+v", got)
	}
}

// TestExtractStatEntrySurvivesJSONHop guards the agent-broker path.
// The CLI re-decodes the broker response into map[string]any, so the
// typed *fsproto.DirEntry stored by StatHandler degrades into a plain
// map. extractStatEntry must accept that shape too, or `agbridge stat`
// fails for every target whenever a broker is running.
func TestExtractStatEntrySurvivesJSONHop(t *testing.T) {
	raw, err := json.Marshal(statHandlerResult())
	if err != nil {
		t.Fatal(err)
	}
	var hopped map[string]any
	if err := json.Unmarshal(raw, &hopped); err != nil {
		t.Fatal(err)
	}
	// Sanity-check the premise: the JSON hop really does erase the type.
	meta, _ := hopped["_meta"].(map[string]any)
	if _, typed := meta["entry"].(*fsproto.DirEntry); typed {
		t.Fatal("test setup: JSON hop unexpectedly preserved the typed entry")
	}

	got, ok := extractStatEntry(hopped)
	if !ok {
		t.Fatal("extractStatEntry rejected the JSON-hop shape")
	}
	if got.Name != "notes.txt" || got.Type != "file" || got.Size != 42 ||
		got.Mode != 0o644 || got.ModTime != 1700000000 {
		t.Fatalf("entry = %+v", got)
	}
}

func TestExtractStatEntryRejectsGarbage(t *testing.T) {
	cases := map[string]map[string]any{
		"no meta":       {"content": []any{}},
		"meta not map":  {"_meta": "nope"},
		"entry missing": {"_meta": map[string]any{}},
		"entry null":    {"_meta": map[string]any{"entry": nil}},
		"entry string":  {"_meta": map[string]any{"entry": "notes.txt"}},
	}
	for name, res := range cases {
		if _, ok := extractStatEntry(res); ok {
			t.Errorf("%s: extractStatEntry accepted a malformed envelope", name)
		}
	}
}
