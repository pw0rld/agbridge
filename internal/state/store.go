package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pw0rld/agbridge/internal/filelock"
)

// SaveBridge writes BridgeState to path atomically with 0600 perms.
func SaveBridge(path string, s *BridgeState) error { return saveJSON(path, s) }

// SaveDaemon writes DaemonState atomically with 0600.
func SaveDaemon(path string, s *DaemonState) error { return saveJSON(path, s) }

// SaveGateway writes GatewayState atomically with 0600.
func SaveGateway(path string, s *GatewayState) error { return saveJSON(path, s) }

// MutateBridge serialises a load→modify→save cycle on a bridge state
// file against sibling processes: concurrent CLI invocations (the core
// agbridge scenario is several agents hammering one profile) would
// otherwise lose updates — whoever saves last silently overwrites what
// the others persisted. fn mutates the freshly-loaded state in place;
// a non-nil error from fn aborts without saving.
func MutateBridge(path string, fn func(*BridgeState) error) error {
	lock, err := filelock.Acquire(path, filelock.DefaultTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	st, err := LoadBridge(path)
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	return SaveBridge(path, st)
}

// LoadBridge reads + version-checks a bridge state file.
func LoadBridge(path string) (*BridgeState, error) {
	var s BridgeState
	if err := loadJSON(path, &s); err != nil {
		return nil, err
	}
	if s.Version != CurrentVersion {
		return nil, fmt.Errorf("state: bridge schema version %d, expected %d (re-enroll required)", s.Version, CurrentVersion)
	}
	s.migrateWorkingDirs()
	return &s, nil
}

// migrateWorkingDirs folds the deprecated global working_dir into
// WorkingDirs[default_target] once at load time. The old field applied to
// every target, which corrupted multi-daemon setups; after migration the
// value only shadows the target it was most likely set for, and every
// other target bootstraps from its own daemon's default_working_dir.
func (s *BridgeState) migrateWorkingDirs() {
	if s.WorkingDir == "" {
		return
	}
	if s.DefaultTarget != "" {
		if s.WorkingDirs == nil {
			s.WorkingDirs = map[string]string{}
		}
		if _, exists := s.WorkingDirs[s.DefaultTarget]; !exists {
			s.WorkingDirs[s.DefaultTarget] = s.WorkingDir
		}
	}
	s.WorkingDir = ""
}

// LoadDaemon reads + version-checks a daemon state file.
func LoadDaemon(path string) (*DaemonState, error) {
	var s DaemonState
	if err := loadJSON(path, &s); err != nil {
		return nil, err
	}
	if s.Version != CurrentVersion {
		return nil, fmt.Errorf("state: daemon schema version %d, expected %d (re-enroll required)", s.Version, CurrentVersion)
	}
	return &s, nil
}

// LoadGateway reads + version-checks a gateway state file.
func LoadGateway(path string) (*GatewayState, error) {
	var s GatewayState
	if err := loadJSON(path, &s); err != nil {
		return nil, err
	}
	if s.Version != CurrentVersion {
		return nil, fmt.Errorf("state: gateway schema version %d, expected %d", s.Version, CurrentVersion)
	}
	return &s, nil
}

func saveJSON(path string, v any) error {
	if path == "" {
		return errors.New("state: empty path")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("state: mkdir %s: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: tempfile: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: marshal: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("state: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("state: rename %s: %w", path, err)
	}
	return nil
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("state: read %s: %w", path, err)
	}
	if len(b) == 0 {
		return errors.New("state: empty file")
	}
	return json.Unmarshal(b, v)
}
