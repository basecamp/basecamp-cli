package data

import (
	"sync"
	"time"
)

// CacheEntry holds a cached value with timing metadata.
type CacheEntry struct {
	Value     any
	FetchedAt time.Time
	TTL       time.Duration // fresh duration
	StaleTTL  time.Duration // serve-stale duration (after TTL expires)
}

// IsFresh returns true if the entry is within its TTL.
func (e *CacheEntry) IsFresh() bool {
	return time.Since(e.FetchedAt) < e.TTL
}

// IsUsable returns true if the entry can be served (fresh or stale).
func (e *CacheEntry) IsUsable() bool {
	return time.Since(e.FetchedAt) < e.TTL+e.StaleTTL
}

// Cache is a simple in-memory key-value cache with TTL support.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
}

// NewCache creates a new cache.
func NewCache() *Cache {
	return &Cache{entries: make(map[string]*CacheEntry)}
}

// Get returns a cached entry if it exists and is usable. Returns nil otherwise.
func (c *Cache) Get(key string) *CacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || !entry.IsUsable() {
		return nil
	}
	return entry
}

// Set stores a value in the cache with the given TTLs.
func (c *Cache) Set(key string, value any, ttl, staleTTL time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &CacheEntry{
		Value:     value,
		FetchedAt: time.Now(),
		TTL:       ttl,
		StaleTTL:  staleTTL,
	}
}

// Invalidate removes a specific key.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Clear removes all entries.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}
