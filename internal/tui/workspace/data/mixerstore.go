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

// MixerVolumes stores per-room volume levels.
type MixerVolumes struct {
	Volumes map[string]int `json:"volumes"` // room key -> volume level (0-4)
}

// MixerStore persists mixer volume settings to disk.
type MixerStore struct {
	dir string
}

// NewMixerStore creates a MixerStore backed by the given cache directory.
func NewMixerStore(cacheDir string) *MixerStore {
	return &MixerStore{dir: filepath.Join(cacheDir, "bonfire")}
}

// Load reads mixer volumes from disk.
func (ms *MixerStore) Load() (MixerVolumes, error) {
	lock, err := ms.acquireLock()
	if err != nil {
		return MixerVolumes{}, err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}

	data, err := os.ReadFile(ms.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return MixerVolumes{Volumes: make(map[string]int)}, nil
		}
		return MixerVolumes{}, err
	}

	var v MixerVolumes
	if err := json.Unmarshal(data, &v); err != nil {
		return MixerVolumes{Volumes: make(map[string]int)}, nil
	}
	if v.Volumes == nil {
		v.Volumes = make(map[string]int)
	}
	return v, nil
}

// Save writes mixer volumes to disk atomically.
func (ms *MixerStore) Save(v MixerVolumes) error {
	if err := os.MkdirAll(ms.dir, 0700); err != nil {
		return err
	}

	lock, err := ms.acquireLock()
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := fmt.Sprintf("%s.%d.%d.tmp", ms.filePath(), os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(ms.filePath())
	}
	if err := os.Rename(tmpPath, ms.filePath()); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (ms *MixerStore) filePath() string {
	return filepath.Join(ms.dir, "mixer.json")
}

func (ms *MixerStore) lockPath() string {
	return filepath.Join(ms.dir, ".mixer.lock")
}

func (ms *MixerStore) acquireLock() (*flock.Flock, error) {
	if err := os.MkdirAll(ms.dir, 0700); err != nil {
		return nil, err
	}
	fl := flock.New(ms.lockPath())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, nil
		}
		return nil, err
	}
	if !locked {
		return nil, nil
	}
	return fl, nil
}
