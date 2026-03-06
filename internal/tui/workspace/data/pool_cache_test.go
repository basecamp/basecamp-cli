package data

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolCacheSaveLoad(t *testing.T) {
	dir := t.TempDir()
	c := NewPoolCache(dir)
	require.NotNil(t, c)

	type item struct {
		Name string `json:"name"`
	}
	now := time.Now().Truncate(time.Millisecond) // JSON loses sub-ms precision
	err := c.Save("test/key:1", item{Name: "hello"}, now)
	require.NoError(t, err)

	var got item
	fetchedAt, ok := c.Load("test/key:1", &got)
	require.True(t, ok)
	assert.Equal(t, "hello", got.Name)
	assert.WithinDuration(t, now, fetchedAt, time.Millisecond)
}

func TestPoolCacheLoadMissing(t *testing.T) {
	c := NewPoolCache(t.TempDir())
	var got int
	_, ok := c.Load("nonexistent", &got)
	assert.False(t, ok)
}

func TestPoolCacheNewEmptyDir(t *testing.T) {
	c := NewPoolCache("")
	assert.Nil(t, c)
}

func TestPoolCacheSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	c := NewPoolCache(dir)

	// Save should not leave temp files behind on success.
	require.NoError(t, c.Save("atomic", "value", time.Now()))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, e := range entries {
		assert.False(t, filepath.Ext(e.Name()) == ".tmp",
			"temp file should not remain: %s", e.Name())
	}

	// Verify the final file exists.
	assert.FileExists(t, c.path("atomic"))
}

func TestPoolCacheSaveOverwrite(t *testing.T) {
	dir := t.TempDir()
	c := NewPoolCache(dir)

	require.NoError(t, c.Save("overwrite", "first", time.Now()))
	require.NoError(t, c.Save("overwrite", "second", time.Now()))

	var got string
	_, ok := c.Load("overwrite", &got)
	require.True(t, ok)
	assert.Equal(t, "second", got)
}

func TestPoolCacheKeySanitization(t *testing.T) {
	dir := t.TempDir()
	c := NewPoolCache(dir)

	require.NoError(t, c.Save("a/b:c", "data", time.Now()))

	// File should use sanitized name.
	expected := filepath.Join(dir, "a_b_c.json")
	assert.FileExists(t, expected)
}
