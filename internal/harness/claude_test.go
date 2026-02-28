package harness

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPluginInstalled_ArrayFormat(t *testing.T) {
	data := []byte(`[{"name": "basecamp", "version": "1.0.0"}]`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_FullQualified(t *testing.T) {
	data := []byte(`[{"package": "basecamp@basecamp", "version": "1.0.0"}]`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_ArrayFormat_NotFound(t *testing.T) {
	data := []byte(`[{"name": "other-plugin", "version": "1.0.0"}]`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat(t *testing.T) {
	data := []byte(`{"basecamp@basecamp": {"version": "1.0.0"}}`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat_Simple(t *testing.T) {
	data := []byte(`{"basecamp": {"version": "1.0.0"}}`)
	assert.True(t, pluginInstalled(data))
}

func TestPluginInstalled_MapFormat_NotFound(t *testing.T) {
	data := []byte(`{"other-plugin": {"version": "1.0.0"}}`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_EmptyArray(t *testing.T) {
	data := []byte(`[]`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_EmptyObject(t *testing.T) {
	data := []byte(`{}`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_InvalidJSON(t *testing.T) {
	data := []byte(`not json`)
	assert.False(t, pluginInstalled(data))
}

func TestPluginInstalled_EmptyData(t *testing.T) {
	data := []byte(``)
	assert.False(t, pluginInstalled(data))
}
