//go:build !darwin && !windows

package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGUISessionAvailableTracksDisplayEnv(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	assert.False(t, guiSessionAvailable())

	t.Setenv("DISPLAY", ":0")
	assert.True(t, guiSessionAvailable())

	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	assert.True(t, guiSessionAvailable(), "Wayland-only sessions have no DISPLAY")
}
