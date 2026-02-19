package data

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_GetMissing(t *testing.T) {
	c := NewCache()
	assert.Nil(t, c.Get("nonexistent"))
}

func TestCache_SetAndGet(t *testing.T) {
	c := NewCache()
	c.Set("key", "value", time.Minute, time.Minute)

	entry := c.Get("key")
	require.NotNil(t, entry)
	assert.Equal(t, "value", entry.Value)
}

func TestCache_IsFresh(t *testing.T) {
	c := NewCache()
	c.Set("key", "value", 50*time.Millisecond, 5*time.Minute)

	entry := c.Get("key")
	require.NotNil(t, entry)
	assert.True(t, entry.IsFresh(), "entry should be fresh immediately after set")
	assert.True(t, entry.IsUsable(), "entry should be usable immediately after set")

	time.Sleep(60 * time.Millisecond)

	entry = c.Get("key")
	require.NotNil(t, entry, "entry should still be gettable while stale")
	assert.False(t, entry.IsFresh(), "entry should no longer be fresh after TTL")
	assert.True(t, entry.IsUsable(), "entry should still be usable within stale window")
}

func TestCache_ExpiresAfterStaleTTL(t *testing.T) {
	c := NewCache()
	c.Set("key", "value", 20*time.Millisecond, 30*time.Millisecond)

	time.Sleep(60 * time.Millisecond)

	assert.Nil(t, c.Get("key"), "entry should be nil after TTL+StaleTTL")
}

func TestCache_Invalidate(t *testing.T) {
	c := NewCache()
	c.Set("a", 1, time.Minute, time.Minute)
	c.Set("b", 2, time.Minute, time.Minute)

	c.Invalidate("a")

	assert.Nil(t, c.Get("a"), "invalidated key should be nil")
	assert.NotNil(t, c.Get("b"), "other key should remain")
}

func TestCache_InvalidateMissing(t *testing.T) {
	c := NewCache()
	// Should not panic
	c.Invalidate("nonexistent")
}

func TestCache_Clear(t *testing.T) {
	c := NewCache()
	c.Set("a", 1, time.Minute, time.Minute)
	c.Set("b", 2, time.Minute, time.Minute)
	c.Set("c", 3, time.Minute, time.Minute)

	c.Clear()

	assert.Nil(t, c.Get("a"))
	assert.Nil(t, c.Get("b"))
	assert.Nil(t, c.Get("c"))
}

func TestCache_SetOverwrites(t *testing.T) {
	c := NewCache()
	c.Set("key", "old", time.Minute, time.Minute)
	c.Set("key", "new", time.Minute, time.Minute)

	entry := c.Get("key")
	require.NotNil(t, entry)
	assert.Equal(t, "new", entry.Value)
}

func TestCache_TypedValues(t *testing.T) {
	c := NewCache()

	type record struct {
		Name string
		ID   int
	}

	items := []record{{Name: "alpha", ID: 1}, {Name: "beta", ID: 2}}
	c.Set("records", items, time.Minute, time.Minute)

	entry := c.Get("records")
	require.NotNil(t, entry)

	got, ok := entry.Value.([]record)
	require.True(t, ok)
	assert.Len(t, got, 2)
	assert.Equal(t, "alpha", got[0].Name)
}
