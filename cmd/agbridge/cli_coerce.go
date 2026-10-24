package main

import (
	"encoding/json"

	"github.com/pw0rld/agbridge/internal/capabilities"
	"github.com/pw0rld/agbridge/internal/fsproto"
)

// Handler results carry typed Go values inside _meta (e.g.
// *fsproto.DirEntry for stat). On the classic one-shot path those
// arrive natively; when the response travels through the agent
// broker's JSON hop they arrive as plain maps. The coercion helpers
// below accept both shapes so every consumer works identically on
// either path. Re-marshalling is cheap and the structs are fully
// JSON-tagged, so the two shapes are provably equivalent.

func coerceDirEntry(v any) (*fsproto.DirEntry, bool) {
	switch e := v.(type) {
	case *fsproto.DirEntry:
		return e, e != nil
	case fsproto.DirEntry:
		return &e, true
	case map[string]any:
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, false
		}
		var d fsproto.DirEntry
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, false
		}
		return &d, true
	}
	return nil, false
}

func coerceDirEntries(v any) ([]fsproto.DirEntry, bool) {
	switch e := v.(type) {
	case []fsproto.DirEntry:
		return e, true
	case []*fsproto.DirEntry:
		out := make([]fsproto.DirEntry, 0, len(e))
		for _, it := range e {
			if it != nil {
				out = append(out, *it)
			}
		}
		return out, true
	case []any:
		out := make([]fsproto.DirEntry, 0, len(e))
		for _, it := range e {
			d, ok := coerceDirEntry(it)
			if !ok {
				return nil, false
			}
			out = append(out, *d)
		}
		return out, true
	}
	return nil, false
}

func coerceCapabilities(v any) (capabilities.Capabilities, bool) {
	switch c := v.(type) {
	case capabilities.Capabilities:
		return c, true
	case *capabilities.Capabilities:
		if c == nil {
			return capabilities.Capabilities{}, false
		}
		return *c, true
	case map[string]any:
		raw, err := json.Marshal(c)
		if err != nil {
			return capabilities.Capabilities{}, false
		}
		got, err := capabilities.Decode(raw)
		if err != nil {
			return capabilities.Capabilities{}, false
		}
		return got, true
	}
	return capabilities.Capabilities{}, false
}
