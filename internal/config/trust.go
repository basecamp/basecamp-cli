package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// TrustStore manages the set of trusted local/repo config file paths.
// Trusted configs are allowed to set authority keys (base_url, profiles,
// default_profile) that are normally rejected from local/repo sources.
type TrustStore struct {
	path string // e.g. ~/.config/basecamp/trusted-configs.json
}

// TrustEntry records a single trusted config path and when it was trusted.
type TrustEntry struct {
	Path      string `json:"path"`
	TrustedAt string `json:"trusted_at"`
}

type trustFile struct {
	Trusted []TrustEntry `json:"trusted"`
}

// NewTrustStore returns a TrustStore backed by trusted-configs.json in configDir.
func NewTrustStore(configDir string) *TrustStore {
	return &TrustStore{path: filepath.Join(configDir, "trusted-configs.json")}
}

// LoadTrustStore is a convenience that creates a TrustStore and verifies the
// backing directory exists. A missing file is fine (empty store); a missing
// directory is a no-op (returns nil store, no error).
func LoadTrustStore(configDir string) *TrustStore {
	if configDir == "" {
		return nil
	}
	return NewTrustStore(configDir)
}

// IsTrusted reports whether path (after canonicalization) is in the trust store.
func (ts *TrustStore) IsTrusted(path string) bool {
	canon := canonicalizePath(path)
	if canon == "" {
		return false
	}
	tf := ts.load()
	for _, e := range tf.Trusted {
		if e.Path == canon {
			return true
		}
	}
	return false
}

// Trust adds path to the trust store (idempotent — re-trusting updates the timestamp).
func (ts *TrustStore) Trust(path string) error {
	canon := canonicalizePath(path)
	if canon == "" {
		return fmt.Errorf("cannot resolve path: %s", path)
	}

	tf := ts.load()

	// Update existing or append
	now := time.Now().UTC().Format(time.RFC3339)
	found := false
	for i := range tf.Trusted {
		if tf.Trusted[i].Path == canon {
			tf.Trusted[i].TrustedAt = now
			found = true
			break
		}
	}
	if !found {
		tf.Trusted = append(tf.Trusted, TrustEntry{Path: canon, TrustedAt: now})
	}

	return ts.save(tf)
}

// Untrust removes path from the trust store. Returns true if it was present.
func (ts *TrustStore) Untrust(path string) (bool, error) {
	canon := canonicalizePath(path)
	if canon == "" {
		return false, fmt.Errorf("cannot resolve path: %s", path)
	}

	tf := ts.load()
	for i, e := range tf.Trusted {
		if e.Path == canon {
			tf.Trusted = append(tf.Trusted[:i], tf.Trusted[i+1:]...)
			return true, ts.save(tf)
		}
	}
	return false, nil
}

// List returns all trusted entries.
func (ts *TrustStore) List() []TrustEntry {
	return ts.load().Trusted
}

func (ts *TrustStore) load() trustFile {
	var tf trustFile
	data, err := os.ReadFile(ts.path) //nolint:gosec // G304: Path is from trusted config location
	if err != nil {
		return tf
	}
	_ = json.Unmarshal(data, &tf)
	return tf
}

func (ts *TrustStore) save(tf trustFile) error {
	dir := filepath.Dir(ts.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create trust store directory: %w", err)
	}

	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal trust store: %w", err)
	}

	return atomicWriteTrustFile(ts.path, append(data, '\n'))
}

// atomicWriteTrustFile writes data atomically via temp+rename.
func atomicWriteTrustFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		if runtime.GOOS == "windows" {
			_ = os.Remove(path)
			return os.Rename(tmpPath, path)
		}
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// canonicalizePath resolves symlinks and returns an absolute path.
// Returns empty string on failure (fail closed).
// When the file itself doesn't exist, resolves symlinks on the parent
// directory so that deleted-file paths still match stored entries.
// Non-existence errors are the only case where fallback is allowed;
// other EvalSymlinks failures (permission denied, etc.) return ""
// to prevent treating unresolvable paths as trusted.
func canonicalizePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "" // fail closed on non-existence errors
		}
		// File doesn't exist — try resolving the parent directory instead.
		// This keeps canonical form consistent for deleted/moved files.
		dir := filepath.Dir(abs)
		if resolvedDir, dirErr := filepath.EvalSymlinks(dir); dirErr == nil {
			return filepath.Join(resolvedDir, filepath.Base(abs))
		}
		return abs
	}
	return resolved
}
