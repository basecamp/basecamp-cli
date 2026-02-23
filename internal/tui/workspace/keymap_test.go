package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadKeyOverrides_MissingFile(t *testing.T) {
	overrides, err := LoadKeyOverrides("/nonexistent/path/keybindings.json")
	require.NoError(t, err)
	assert.Nil(t, overrides)
}

func TestLoadKeyOverrides_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	err := os.WriteFile(path, []byte(`{"hey": "ctrl+h", "jump": "ctrl+k"}`), 0644)
	require.NoError(t, err)

	overrides, err := LoadKeyOverrides(path)
	require.NoError(t, err)
	assert.Equal(t, "ctrl+h", overrides["hey"])
	assert.Equal(t, "ctrl+k", overrides["jump"])
}

func TestLoadKeyOverrides_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keybindings.json")
	err := os.WriteFile(path, []byte(`{not json`), 0644)
	require.NoError(t, err)

	_, err = LoadKeyOverrides(path)
	assert.Error(t, err)
}

func TestApplyOverrides_RemapsKey(t *testing.T) {
	km := DefaultGlobalKeyMap()

	// Default is ctrl+y
	assert.Equal(t, "ctrl+y", km.Hey.Help().Key, "default Hey help key should be ctrl+y")

	ApplyOverrides(&km, map[string]string{"hey": "ctrl+h"})

	// After override, help text should reflect new key
	assert.Equal(t, "ctrl+h", km.Hey.Help().Key, "Hey help key should be ctrl+h after override")
	assert.Equal(t, "hey! inbox", km.Hey.Help().Desc, "Hey description should be preserved")

	// The binding's keys should contain the new key
	keys := km.Hey.Keys()
	assert.Contains(t, keys, "ctrl+h", "Hey keys should contain ctrl+h")
}

func TestApplyOverrides_UnknownAction_Ignored(t *testing.T) {
	km := DefaultGlobalKeyMap()
	original := km.Hey.Help().Key

	ApplyOverrides(&km, map[string]string{"nonexistent_action": "ctrl+z"})

	assert.Equal(t, original, km.Hey.Help().Key, "Hey should be unchanged")
}

func TestApplyOverrides_MultipleActions(t *testing.T) {
	km := DefaultGlobalKeyMap()

	ApplyOverrides(&km, map[string]string{
		"hey":       "ctrl+h",
		"my_stuff":  "ctrl+m",
		"activity":  "ctrl+w",
		"bogus_key": "ctrl+z", // unknown, should be ignored
	})

	assert.Equal(t, "ctrl+h", km.Hey.Help().Key)
	assert.Equal(t, "ctrl+m", km.MyStuff.Help().Key)
	assert.Equal(t, "ctrl+w", km.Activity.Help().Key)
	// Verify others unchanged
	assert.Equal(t, "ctrl+p", km.Palette.Help().Key)
}

func TestDefaultGlobalKeyMap_HeyIsCtrlY(t *testing.T) {
	km := DefaultGlobalKeyMap()
	help := km.Hey.Help()
	assert.Equal(t, "ctrl+y", help.Key, "Hey default should be ctrl+y, not ctrl+h")
}

// key.Binding.Keys() returns the key strings for direct assertion.
func TestApplyOverrides_BindingKeysUpdated(t *testing.T) {
	km := DefaultGlobalKeyMap()
	ApplyOverrides(&km, map[string]string{"jump": "ctrl+k"})

	keys := km.Jump.Keys()
	require.Len(t, keys, 1)
	assert.Equal(t, "ctrl+k", keys[0])
}
