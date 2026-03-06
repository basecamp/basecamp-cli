package data

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PoolCache provides disk-backed persistence for pool snapshots.
// On successful fetch, data is written to disk. On pool creation,
// cached data seeds the snapshot as Stale so the TUI boots into
// real screens while background refresh runs.
type PoolCache struct {
	dir string // e.g. ~/.cache/basecamp/pools/
}

// NewPoolCache creates a cache backed by the given directory.
// Returns nil if dir is empty.
func NewPoolCache(dir string) *PoolCache {
	if dir == "" {
		return nil
	}
	return &PoolCache{dir: dir}
}

type cacheEnvelope struct {
	Data      json.RawMessage `json:"data"`
	FetchedAt time.Time       `json:"fetched_at"`
}

// Save writes pool data to disk as JSON.
func (c *PoolCache) Save(key string, data any, fetchedAt time.Time) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	env := cacheEnvelope{Data: raw, FetchedAt: fetchedAt}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	// Write via temp file + rename to avoid partial reads.
	// On POSIX this is atomic; on Windows there is a brief window
	// where the file is absent (remove before rename) — acceptable
	// for a best-effort cache where Load simply returns false.
	dst := c.path(key)
	tmp := fmt.Sprintf("%s.%d.%d.tmp", dst, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(dst)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Load reads cached data into dst. Returns fetchedAt and whether cache exists.
func (c *PoolCache) Load(key string, dst any) (time.Time, bool) {
	b, err := os.ReadFile(c.path(key))
	if err != nil {
		return time.Time{}, false
	}
	var env cacheEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return time.Time{}, false
	}
	if err := json.Unmarshal(env.Data, dst); err != nil {
		return time.Time{}, false
	}
	return env.FetchedAt, true
}

func (c *PoolCache) path(key string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", "\\", "_", "..", "_").Replace(key)
	return filepath.Join(c.dir, filepath.Base(safe)+".json")
}
