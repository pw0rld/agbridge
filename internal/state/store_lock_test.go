package state

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// TestMutateBridgeNoLostUpdates models the core agbridge scenario:
// several CLI processes mutating one profile concurrently. Each loads
// fresh under the cross-process lock, so every mutation must survive
// to the final file — an unlocked load→modify→save would let the last
// writer clobber the others.
func TestMutateBridgeNoLostUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seed := &BridgeState{Version: CurrentVersion, WorkingDirs: map[string]string{}}
	if err := SaveBridge(path, seed); err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := MutateBridge(path, func(st *BridgeState) error {
				if st.WorkingDirs == nil {
					st.WorkingDirs = map[string]string{}
				}
				st.WorkingDirs[fmt.Sprintf("d%d", i)] = fmt.Sprintf("/x%d", i)
				return nil
			})
			if err != nil {
				t.Errorf("mutate %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := LoadBridge(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WorkingDirs) != n {
		t.Fatalf("lost updates: %d of %d survived: %+v", len(got.WorkingDirs), n, got.WorkingDirs)
	}
}

// TestMutateBridgeAbortsOnError: fn errors must not persist.
func TestMutateBridgeAbortsOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seed := &BridgeState{Version: CurrentVersion, DefaultTarget: "keep"}
	if err := SaveBridge(path, seed); err != nil {
		t.Fatal(err)
	}
	err := MutateBridge(path, func(st *BridgeState) error {
		st.DefaultTarget = "changed"
		return fmt.Errorf("nope")
	})
	if err == nil {
		t.Fatal("expected error propagation")
	}
	got, lerr := LoadBridge(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got.DefaultTarget != "keep" {
		t.Fatalf("aborted mutation leaked to disk: %q", got.DefaultTarget)
	}
}
