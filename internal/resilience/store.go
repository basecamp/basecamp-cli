// Package resilience provides cross-process state management for long-running
// CLI operations. It enables resumable operations by persisting state to disk
// with proper file locking for safe concurrent access.
package resilience

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
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
func defaultStateDir() string {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheDir, "bcq", DefaultDirName)
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

// fileLock represents an acquired file lock.
type fileLock struct {
	file *os.File
}

// acquireLock obtains an exclusive lock on the state directory.
// The caller must call release() when done.
func (s *Store) acquireLock() (*fileLock, error) {
	// Ensure directory exists
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	// Acquire exclusive lock (blocking)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}

	return &fileLock{file: f}, nil
}

// release releases the file lock.
func (fl *fileLock) release() error {
	if fl.file == nil {
		return nil
	}
	// Unlock then close - ignore unlock error as we're closing anyway
	_ = syscall.Flock(int(fl.file.Fd()), syscall.LOCK_UN)
	return fl.file.Close()
}

// Load reads the state from disk with proper locking.
// Returns an empty state if the file doesn't exist.
func (s *Store) Load() (*State, error) {
	lock, err := s.acquireLock()
	if err != nil {
		return nil, err
	}
	defer lock.release()

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
func (s *Store) Save(state *State) error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer lock.release()

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

	// Write atomically via temp file
	tmpPath := s.Path() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}

	return os.Rename(tmpPath, s.Path())
}

// Update atomically loads, modifies, and saves the state.
// The updateFn receives the current state and should modify it in place.
// This is the preferred way to update state as it holds the lock
// throughout the entire read-modify-write cycle.
func (s *Store) Update(updateFn func(*State) error) error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer lock.release()

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
func (s *Store) Clear() error {
	lock, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer lock.release()

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
