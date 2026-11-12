package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// TrustStore manages the set of trusted local/repo config files.
// Trusted configs are allowed to set authority keys (base_url, profiles,
// default_profile) that are normally rejected from local/repo sources.
// Trust is given to a file's trust-gated settings as they were when it was
// trusted: a checkout or pull that changes them leaves the file untrusted
// until it is trusted again.
type TrustStore struct {
	path string // e.g. ~/.config/basecamp/trusted-configs.json
}

// TrustEntry records a single trusted config path, when it was trusted, and
// a digest of the trust-gated settings it held then. An entry without a
// digest, written before trust was bound to content, trusts nothing.
type TrustEntry struct {
	Path      string `json:"path"`
	TrustedAt string `json:"trusted_at"`
	Digest    string `json:"digest,omitempty"`
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

// IsTrusted reports whether the config file at path is trusted as it is now.
func (ts *TrustStore) IsTrusted(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // G304: Path is a config file the caller is about to load
	if err != nil {
		return false
	}
	return ts.IsTrustedContent(path, data)
}

// IsTrustedContent reports whether path (after canonicalization) is in the
// trust store with data's trust-gated settings, data being the file's
// contents as the caller read them.
func (ts *TrustStore) IsTrustedContent(path string, data []byte) bool {
	canon := canonicalizePath(path)
	if canon == "" {
		return false
	}
	digest, err := trustDigest(data)
	if err != nil {
		return false
	}
	tf := ts.load()
	for _, e := range tf.Trusted {
		if e.Path == canon {
			return e.Digest == digest
		}
	}
	return false
}

// Trust adds path to the trust store with its current trust-gated settings
// (idempotent — re-trusting updates the timestamp and the digest).
func (ts *TrustStore) Trust(path string) error {
	canon := canonicalizePath(path)
	if canon == "" {
		return fmt.Errorf("cannot resolve path: %s", path)
	}
	data, err := os.ReadFile(canon) //nolint:gosec // G304: Path is the config file the user asked to trust
	if err != nil {
		return fmt.Errorf("cannot read config: %w", err)
	}
	digest, err := trustDigest(data)
	if err != nil {
		return fmt.Errorf("cannot trust %s: %w", path, err)
	}

	tf := ts.load()

	// Update existing or append
	now := time.Now().UTC().Format(time.RFC3339)
	found := false
	for i := range tf.Trusted {
		if tf.Trusted[i].Path == canon {
			tf.Trusted[i].TrustedAt = now
			tf.Trusted[i].Digest = digest
			found = true
			break
		}
	}
	if !found {
		tf.Trusted = append(tf.Trusted, TrustEntry{Path: canon, TrustedAt: now, Digest: digest})
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

// untrustedKeys are the keys loadFromFile takes from a local or repo config
// whether or not it is trusted. Trust adds nothing to them, so changing one
// does not take a file's trust away. Every other key, including any this
// version does not know, is part of what is trusted. A key gated by trust
// must never be listed here.
var untrustedKeys = []string{
	"account_id", "project_id", "todolist_id", "scope",
	"format", "hints", "stats", "onboarded", "verbose", "experimental",
}

// trustDigest is the SHA-256 of a config file's trust-gated settings: the
// JSON object without untrustedKeys, re-encoded with sorted keys so that
// formatting and key order do not change it.
func trustDigest(data []byte) (string, error) {
	var fileCfg map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&fileCfg); err != nil {
		return "", fmt.Errorf("not a JSON object: %w", err)
	}
	if fileCfg == nil {
		return "", errors.New("not a JSON object")
	}
	for _, key := range untrustedKeys {
		delete(fileCfg, key)
	}
	canonical, err := json.Marshal(fileCfg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
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
