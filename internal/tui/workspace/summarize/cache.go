package summarize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cacheEntry holds a cached summary result.
type cacheEntry struct {
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

// SummaryCache provides disk-backed caching with in-memory eviction (oldest-first).
type SummaryCache struct {
	mu     sync.RWMutex
	dir    string // disk cache directory
	memory map[string]cacheEntry
	ttl    time.Duration
	maxMem int
}

// NewSummaryCache creates a cache backed by the given directory.
func NewSummaryCache(dir string, ttl time.Duration, maxMemEntries int) *SummaryCache {
	return &SummaryCache{
		dir:    dir,
		memory: make(map[string]cacheEntry),
		ttl:    ttl,
		maxMem: maxMemEntries,
	}
}

// CacheKey computes a content-aware cache key.
// key = sha256(contentKey + ":" + zoomBucket + ":" + contentHash)
func CacheKey(contentKey string, zoom ZoomLevel, contentHash string) string {
	raw := fmt.Sprintf("%s:%d:%s", contentKey, zoom, contentHash)
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// Get looks up a cached summary. Returns ("", false) on miss.
func (c *SummaryCache) Get(key string) (string, bool) {
	c.mu.RLock()
	if entry, ok := c.memory[key]; ok {
		if time.Since(entry.CreatedAt) < c.ttl {
			c.mu.RUnlock()
			return entry.Summary, true
		}
	}
	c.mu.RUnlock()

	// Try disk
	entry, err := c.readDisk(key)
	if err != nil || time.Since(entry.CreatedAt) >= c.ttl {
		return "", false
	}

	// Promote to memory
	c.mu.Lock()
	c.memory[key] = entry
	c.evictIfNeeded()
	c.mu.Unlock()
	return entry.Summary, true
}

// Put stores a summary in both memory and disk cache.
func (c *SummaryCache) Put(key, summary string) {
	entry := cacheEntry{Summary: summary, CreatedAt: time.Now()}
	c.mu.Lock()
	c.memory[key] = entry
	c.evictIfNeeded()
	c.mu.Unlock()

	_ = c.writeDisk(key, entry) // best-effort
}

func (c *SummaryCache) readDisk(key string) (cacheEntry, error) {
	path := filepath.Join(c.dir, key+".json")
	data, err := os.ReadFile(path) //nolint:gosec // path from internal key
	if err != nil {
		return cacheEntry{}, err
	}
	var entry cacheEntry
	err = json.Unmarshal(data, &entry)
	return entry, err
}

func (c *SummaryCache) writeDisk(key string, entry cacheEntry) error {
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	path := filepath.Join(c.dir, key+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *SummaryCache) evictIfNeeded() {
	if len(c.memory) <= c.maxMem {
		return
	}
	// Simple eviction: remove oldest entry
	var oldestKey string
	var oldestTime time.Time
	for k, v := range c.memory {
		if oldestKey == "" || v.CreatedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.CreatedAt
		}
	}
	if oldestKey != "" {
		delete(c.memory, oldestKey)
	}
}
