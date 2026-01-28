// Package resilience provides cross-process state management for long-running
// CLI operations. It enables resumable operations by persisting state to disk
// with proper file locking for safe concurrent access.
package resilience

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

const (
	// StateFileName is the default state file name.
	StateFileName = "state.json"

	// DefaultDirName is the subdirectory within the cache dir.
	DefaultDirName = "resilience"
)

// Store handles reading and writing resilience state with file locking.
// It provides atomic operations safe for concurrent access across processes.
type Store struct {
	dir string
}

// NewStore creates a new resilience state store.
// If dir is empty, it uses the default location (~/.cache/bcq/resilience/).
func NewStore(dir string) *Store {
	if dir == "" {
		dir = defaultStateDir()
	}
	return &Store{dir: dir}
}

// defaultStateDir returns the default state directory path.
// Uses platform-specific cache directories with proper fallbacks.
func defaultStateDir() string {
	// First, check XDG_CACHE_HOME (Linux/BSD convention)
	if cacheDir := os.Getenv("XDG_CACHE_HOME"); cacheDir != "" {
		return filepath.Join(cacheDir, "bcq", DefaultDirName)
	}

	// Use os.UserCacheDir() which handles platform-specific paths:
	// - macOS: ~/Library/Caches
	// - Linux: ~/.cache (respects XDG_CACHE_HOME)
	// - Windows: %LocalAppData%
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "bcq", DefaultDirName)
	}

	// Fall back to home directory
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "bcq", DefaultDirName)
	}

	// Last resort: use temp directory to avoid relative paths
	return filepath.Join(os.TempDir(), "bcq", DefaultDirName)
}

// Dir returns the state directory path.
func (s *Store) Dir() string {
	return s.dir
}

// Path returns the full path to the state file.
func (s *Store) Path() string {
	return filepath.Join(s.dir, StateFileName)
}

// lockPath returns the path to the lock file.
func (s *Store) lockPath() string {
	return filepath.Join(s.dir, ".lock")
}

// LockTimeout is the maximum time to wait for acquiring the file lock.
// If exceeded, operations proceed without locking (fail-open) to avoid CLI hangs.
const LockTimeout = 100 * time.Millisecond

// fileLock represents an acquired file lock.
type fileLock struct {
	flock *flock.Flock
}

// acquireLock obtains an exclusive lock on the state directory.
// The caller must call release() when done.
//
// Fail-open semantics: Returns nil (with no error) if the lock cannot be
// acquired within LockTimeout. This is intentional - we prefer brief windows
// of potential race conditions over CLI commands hanging indefinitely when
// another process holds the lock (e.g., crashed process, NFS issues).
//
// The resilience primitives are designed to tolerate occasional state
// inconsistencies: circuit breaker may let a few extra requests through,
// bulkhead may briefly exceed limits, rate limiter may over-count tokens.
// These are acceptable tradeoffs for a CLI tool where user experience
// (no hangs) takes priority over perfect coordination.
func (s *Store) acquireLock() (*fileLock, error) {
	// Ensure directory exists
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, err
	}

	fl := flock.New(s.lockPath())

	// Try to acquire lock with timeout
	ctx, cancel := context.WithTimeout(context.Background(), LockTimeout)
	defer cancel()

	// TryLockContext retries every 10ms until context expires
	locked, err := fl.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		// Only fail-open on context deadline (timeout), not real errors
		if ctx.Err() == context.DeadlineExceeded {
			return nil, nil
		}
		// Real lock error (permissions, filesystem issues) - return error
		return nil, err
	}
	if !locked {
		// Timeout without error - proceed without lock
		return nil, nil
	}

	return &fileLock{flock: fl}, nil
}

// release releases the file lock.
func (fl *fileLock) release() error {
	if fl == nil || fl.flock == nil {
		return nil
	}
	return fl.flock.Unlock()
}

// Load reads the state from disk with proper locking.
// Returns an empty state if the file doesn't exist.
// If the lock cannot be acquired, proceeds without locking (fail-open).
func (s *Store) Load() (*State, error) {
	lock, err := s.acquireLock()
	if err != nil {
		return nil, err
	}
	if lock != nil {
		defer func() { _ = lock.release() }()
	}

	return s.loadUnsafe()
}

// loadUnsafe reads the state without locking (caller must hold lock).
func (s *Store) loadUnsafe() (*State, error) {
	data, err := os.ReadFile(s.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return NewState(), nil
		}
		return nil, err
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		// Invalid JSON - return empty state rather than error
		// This handles corrupted files gracefully
		return NewState(), nil
	}

	return &state, nil
}

// Save writes the state to disk atomically with proper locking.
// If the lock cannot be acquired, proceeds without locking (fail-open).
func (s *Store) Save(state *State) error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.release() }()
	}

	return s.saveUnsafe(state)
}

// saveUnsafe writes the state without locking (caller must hold lock).
func (s *Store) saveUnsafe(state *State) error {
	// Ensure directory exists
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}

	state.Version = StateVersion

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically via temp file with unique name (PID + timestamp)
	// to avoid conflicts when lock cannot be acquired (fail-open scenario)
	tmpPath := fmt.Sprintf("%s.%d.%d.tmp", s.Path(), os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}

	// On Windows, os.Rename fails if destination exists. Remove it first.
	// Note: This creates a brief window where the file doesn't exist. In fail-open
	// scenarios (no lock held), another process may observe the missing file and
	// treat it as a fresh state. This is acceptable given the fail-open design:
	// the resilience primitives tolerate occasional state resets, and the lock
	// is held in the common case. Using MoveFileEx with MOVEFILE_REPLACE_EXISTING
	// would avoid this but adds x/sys/windows dependency.
	if runtime.GOOS == "windows" {
		_ = os.Remove(s.Path()) // Ignore error if file doesn't exist
	}

	if err := os.Rename(tmpPath, s.Path()); err != nil {
		// Clean up temp file on error
		_ = os.Remove(tmpPath)
		return err
	}

	return nil
}

// Update atomically loads, modifies, and saves the state.
// The updateFn receives the current state and should modify it in place.
// This is the preferred way to update state as it holds the lock
// throughout the entire read-modify-write cycle.
// If the lock cannot be acquired, proceeds without locking (fail-open).
func (s *Store) Update(updateFn func(*State) error) error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.release() }()
	}

	state, err := s.loadUnsafe()
	if err != nil {
		return err
	}

	if err := updateFn(state); err != nil {
		return err
	}

	return s.saveUnsafe(state)
}

// Clear removes the state file.
// If the lock cannot be acquired, proceeds without locking (fail-open).
func (s *Store) Clear() error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	if lock != nil {
		defer func() { _ = lock.release() }()
	}

	err = os.Remove(s.Path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Exists returns true if a state file exists.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.Path())
	return err == nil
}
