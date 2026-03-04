package resolve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistValueBooleanKeys(t *testing.T) {
	// Use a temp dir as XDG_CONFIG_HOME so PersistValue writes there
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configPath := filepath.Join(tmpDir, "basecamp", "config.json")

	boolKeys := []string{"onboarded", "hints", "stats", "cache_enabled"}

	for _, key := range boolKeys {
		t.Run(key+"=true", func(t *testing.T) {
			// Start fresh
			require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0700))
			require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))

			err := PersistValue(key, "true", "global")
			require.NoError(t, err)

			data, err := os.ReadFile(configPath)
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(data, &raw))

			val, ok := raw[key]
			require.True(t, ok, "key %q should exist in config", key)
			assert.Equal(t, true, val, "key %q should be boolean true, not string", key)
		})

		t.Run(key+"=false", func(t *testing.T) {
			require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0700))
			require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))

			err := PersistValue(key, "false", "global")
			require.NoError(t, err)

			data, err := os.ReadFile(configPath)
			require.NoError(t, err)

			var raw map[string]any
			require.NoError(t, json.Unmarshal(data, &raw))

			val, ok := raw[key]
			require.True(t, ok, "key %q should exist in config", key)
			assert.Equal(t, false, val, "key %q should be boolean false, not string", key)
		})
	}
}

func TestPersistValueStringKeys(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configPath := filepath.Join(tmpDir, "basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0700))
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))

	err := PersistValue("account_id", "12345", "global")
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	val, ok := raw["account_id"]
	require.True(t, ok)
	assert.Equal(t, "12345", val, "string keys should remain strings")
}

func TestPersistValueBooleanWhitespaceTolerance(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configPath := filepath.Join(tmpDir, "basecamp", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0700))
	require.NoError(t, os.WriteFile(configPath, []byte("{}"), 0600))

	err := PersistValue("onboarded", " true ", "global")
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	val, ok := raw["onboarded"]
	require.True(t, ok)
	assert.Equal(t, true, val, "whitespace-padded 'true' should persist as boolean true")
}
