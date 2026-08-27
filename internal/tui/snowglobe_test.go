package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnowglobeRasterDimensions(t *testing.T) {
	lines := strings.Split(snowglobeRaster, "\n")
	require.Len(t, lines, snowglobeHeight)
	for _, line := range lines {
		assert.LessOrEqual(t, len(line), snowglobeWidth)
	}
}

func TestRenderSnowglobeNoColor(t *testing.T) {
	rendered := RenderSnowglobe(NoColorTheme())
	assert.NotContains(t, rendered, "\x1b[")
	assert.NotContains(t, rendered, "Basecamp")
	assert.Contains(t, rendered, "░")
	assert.Contains(t, rendered, "▓")
	assert.Len(t, strings.Split(rendered, "\n"), snowglobeHeight/2)
}

func TestRenderSnowglobeWithColor(t *testing.T) {
	rendered := RenderSnowglobe(DefaultTheme(true))
	assert.Contains(t, rendered, "\x1b[")
	assert.Contains(t, rendered, "▀")
	assert.NotContains(t, rendered, "Basecamp")
}
