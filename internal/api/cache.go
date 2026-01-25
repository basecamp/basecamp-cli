package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Cache provides ETag-based HTTP caching.
type Cache struct {
	dir string
	mu  sync.RWMutex
}

// NewCache creates a new cache with the given directory.
func NewCache(dir string) *Cache {
	return &Cache{dir: dir}
}

// Key generates a cache key for a URL, account, and token.
// The key includes a token hash to ensure different auth contexts don't share cache.
func (c *Cache) Key(url, accountID, token string) string {
	tokenHash := ""
	if token != "" {
		h := sha256.Sum256([]byte(token))
		tokenHash = hex.EncodeToString(h[:8]) // First 16 chars
	}

	input := url + ":" + accountID + ":" + tokenHash
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}

// GetETag returns the cached ETag for a key, or empty string if not found.
func (c *Cache) GetETag(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	etagsFile := filepath.Join(c.dir, "etags.json")
	data, err := os.ReadFile(etagsFile) //nolint:gosec // G304: Path is from trusted cache dir
	if err != nil {
		return ""
	}

	var etags map[string]string
	if err := json.Unmarshal(data, &etags); err != nil {
		return ""
	}

	return etags[key]
}

// GetBody returns the cached response body for a key, or nil if not found.
func (c *Cache) GetBody(key string) []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()

	bodyFile := filepath.Join(c.dir, "responses", key+".body")
	data, err := os.ReadFile(bodyFile) //nolint:gosec // G304: Path is from trusted cache dir
	if err != nil {
		return nil
	}
	return data
}

// Set stores a response body and ETag for a key.
func (c *Cache) Set(key string, body []byte, etag string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Ensure directories exist
	responsesDir := filepath.Join(c.dir, "responses")
	if err := os.MkdirAll(responsesDir, 0700); err != nil {
		return err
	}

	// Write body atomically
	bodyFile := filepath.Join(responsesDir, key+".body")
	tmpFile := bodyFile + ".tmp"
	if err := os.WriteFile(tmpFile, body, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, bodyFile); err != nil {
		_ = os.Remove(tmpFile) // Best-effort cleanup
		return err
	}

	// Update etags.json
	etagsFile := filepath.Join(c.dir, "etags.json")
	etags := make(map[string]string)

	// Load existing etags
	if data, err := os.ReadFile(etagsFile); err == nil { //nolint:gosec // G304: Path is from trusted cache dir
		_ = json.Unmarshal(data, &etags) // Ignore error - will start with empty map
	}

	etags[key] = etag

	// Write etags atomically
	data, err := json.MarshalIndent(etags, "", "  ")
	if err != nil {
		return err
	}

	tmpEtags := etagsFile + ".tmp"
	if err := os.WriteFile(tmpEtags, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpEtags, etagsFile); err != nil {
		_ = os.Remove(tmpEtags) // Best-effort cleanup
		return err
	}

	return nil
}

// Clear removes all cached data.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	responsesDir := filepath.Join(c.dir, "responses")
	if err := os.RemoveAll(responsesDir); err != nil {
		return err
	}

	etagsFile := filepath.Join(c.dir, "etags.json")
	_ = os.Remove(etagsFile) // Best-effort cleanup

	return nil
}

// Invalidate removes cached data for a specific key.
func (c *Cache) Invalidate(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove body file
	bodyFile := filepath.Join(c.dir, "responses", key+".body")
	_ = os.Remove(bodyFile) // Best-effort cleanup

	// Remove from etags.json
	etagsFile := filepath.Join(c.dir, "etags.json")
	etags := make(map[string]string)

	if data, err := os.ReadFile(etagsFile); err == nil { //nolint:gosec // G304: Path is from trusted cache dir
		_ = json.Unmarshal(data, &etags) // Ignore error - will start with empty map
	}

	delete(etags, key)

	data, err := json.MarshalIndent(etags, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(etagsFile, data, 0600)
}
