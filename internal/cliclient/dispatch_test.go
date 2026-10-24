package cliclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatchUnknownHandler(t *testing.T) {
	r := &Router{}
	_, err := r.Dispatch(context.Background(), "bogus", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "unknown_handler") {
		t.Fatalf("Dispatch(bogus) err = %v, want unknown_handler", err)
	}
}
