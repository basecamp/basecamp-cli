package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// ReadTracker tracks the last-read line ID per campfire room.
type ReadTracker struct {
	mu    sync.Mutex
	dir   string
	local map[string]int64 // in-memory max per room key
}

// NewReadTracker creates a ReadTracker backed by the given cache directory.
func NewReadTracker(cacheDir string) *ReadTracker {
	return &ReadTracker{
		dir:   filepath.Join(cacheDir, "bonfire"),
		local: make(map[string]int64),
	}
}

// MarkRead records a line as read. Keeps the max of local and lineID.
func (rt *ReadTracker) MarkRead(room RoomID, lineID int64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	key := room.Key()
	if lineID > rt.local[key] {
		rt.local[key] = lineID
	}
}

// LastRead returns the last-read line ID for a room.
func (rt *ReadTracker) LastRead(room RoomID) int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.local[room.Key()]
}

// UnreadCount returns the number of lines after the last-read position.
func (rt *ReadTracker) UnreadCount(room RoomID, lines []CampfireLineInfo) int {
	rt.mu.Lock()
	lastID := rt.local[room.Key()]
	rt.mu.Unlock()

	count := 0
	for _, line := range lines {
		if line.ID > lastID {
			count++
		}
	}
	return count
}

// Flush merges local state with disk (max per room) and writes atomically.
func (rt *ReadTracker) Flush() error {
	rt.mu.Lock()
	snapshot := make(map[string]int64, len(rt.local))
	for k, v := range rt.local {
		snapshot[k] = v
	}
	rt.mu.Unlock()

	if len(snapshot) == 0 {
		return nil
	}

	if err := os.MkdirAll(rt.dir, 0700); err != nil {
		return err
	}

	lock, err := rt.acquireLock()
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.Unlock() }()
	}

	// Read existing
	disk := make(map[string]int64)
	if data, err := os.ReadFile(rt.filePath()); err == nil {
		_ = json.Unmarshal(data, &disk)
	}

	// Merge: max per room
	for k, v := range snapshot {
		if v > disk[k] {
			disk[k] = v
		}
	}

	// Update local with merged values
	rt.mu.Lock()
	for k, v := range disk {
		if v > rt.local[k] {
			rt.local[k] = v
		}
	}
	rt.mu.Unlock()

	// Atomic write
	data, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.%d.%d.tmp", rt.filePath(), os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(rt.filePath())
	}
	if err := os.Rename(tmpPath, rt.filePath()); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// LoadFromDisk reads persisted read positions into memory.
func (rt *ReadTracker) LoadFromDisk() {
	data, err := os.ReadFile(rt.filePath())
	if err != nil {
		return
	}
	disk := make(map[string]int64)
	if err := json.Unmarshal(data, &disk); err != nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for k, v := range disk {
		if v > rt.local[k] {
			rt.local[k] = v
		}
	}
}

func (rt *ReadTracker) filePath() string {
	return filepath.Join(rt.dir, "readstate.json")
}

func (rt *ReadTracker) lockPath() string {
	return filepath.Join(rt.dir, ".readstate.lock")
}

func (rt *ReadTracker) acquireLock() (*flock.Flock, error) {
	fl := flock.New(rt.lockPath())
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
