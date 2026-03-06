package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

// RoomOverride stores user room selection preferences.
type RoomOverride struct {
	Includes map[string]RoomOverrideEntry `json:"includes,omitempty"`
	Excludes map[string]RoomOverrideEntry `json:"excludes,omitempty"`
}

// RoomOverrideEntry records when a room was added/excluded.
type RoomOverrideEntry struct {
	Name      string `json:"name,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// RoomStore persists room selection overrides to disk.
type RoomStore struct {
	dir string
}

// NewRoomStore creates a RoomStore backed by the given cache directory.
func NewRoomStore(cacheDir string) *RoomStore {
	return &RoomStore{dir: filepath.Join(cacheDir, "bonfire")}
}

// Load reads the room override file with file locking.
func (rs *RoomStore) Load(ctx context.Context) (RoomOverride, error) {
	lock, err := rs.acquireLock(ctx)
	if err != nil {
		return RoomOverride{}, err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}

	data, err := os.ReadFile(rs.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return RoomOverride{}, nil
		}
		return RoomOverride{}, err
	}

	var o RoomOverride
	if err := json.Unmarshal(data, &o); err != nil {
		return RoomOverride{}, nil // corrupt file → empty
	}
	return o, nil
}

// Save writes room overrides with read-merge-write and atomic rename.
func (rs *RoomStore) Save(ctx context.Context, o RoomOverride) error {
	if err := os.MkdirAll(rs.dir, 0700); err != nil {
		return err
	}

	lock, err := rs.acquireLock(ctx)
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}

	// Read-merge-write: merge with existing data on disk
	existing := RoomOverride{}
	if data, err := os.ReadFile(rs.filePath()); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	merged := mergeOverrides(existing, o)

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write via temp file + rename
	tmpPath := fmt.Sprintf("%s.%d.%d.tmp", rs.filePath(), os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(rs.filePath())
	}
	if err := os.Rename(tmpPath, rs.filePath()); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// EffectiveRooms filters discovered rooms through overrides.
// Includes override: only those rooms. Excludes override: remove those rooms.
// If no overrides, returns all discovered rooms.
func (rs *RoomStore) EffectiveRooms(discovered []BonfireRoomConfig, o RoomOverride) []BonfireRoomConfig {
	if len(o.Includes) > 0 {
		var result []BonfireRoomConfig
		for _, room := range discovered {
			if _, ok := o.Includes[room.Key()]; ok {
				result = append(result, room)
			}
		}
		return result
	}
	if len(o.Excludes) > 0 {
		var result []BonfireRoomConfig
		for _, room := range discovered {
			if _, ok := o.Excludes[room.Key()]; !ok {
				result = append(result, room)
			}
		}
		return result
	}
	return discovered
}

func (rs *RoomStore) filePath() string {
	return filepath.Join(rs.dir, "rooms.json")
}

func (rs *RoomStore) lockPath() string {
	return filepath.Join(rs.dir, ".rooms.lock")
}

func (rs *RoomStore) acquireLock(ctx context.Context) (*flock.Flock, error) {
	if err := os.MkdirAll(rs.dir, 0700); err != nil {
		return nil, err
	}
	fl := flock.New(rs.lockPath())
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, nil // fail-open
		}
		return nil, err
	}
	if !locked {
		return nil, nil // fail-open
	}
	return fl, nil
}

// mergeOverrides merges new overrides into existing using LWW per entry.
func mergeOverrides(existing, incoming RoomOverride) RoomOverride {
	result := RoomOverride{
		Includes: make(map[string]RoomOverrideEntry),
		Excludes: make(map[string]RoomOverrideEntry),
	}
	// Copy existing
	for k, v := range existing.Includes {
		result.Includes[k] = v
	}
	for k, v := range existing.Excludes {
		result.Excludes[k] = v
	}
	// Merge incoming with LWW
	for k, v := range incoming.Includes {
		if e, ok := result.Includes[k]; !ok || v.UpdatedAt >= e.UpdatedAt {
			result.Includes[k] = v
		}
	}
	for k, v := range incoming.Excludes {
		if e, ok := result.Excludes[k]; !ok || v.UpdatedAt >= e.UpdatedAt {
			result.Excludes[k] = v
		}
	}
	// Clean up empty maps
	if len(result.Includes) == 0 {
		result.Includes = nil
	}
	if len(result.Excludes) == 0 {
		result.Excludes = nil
	}
	return result
}
