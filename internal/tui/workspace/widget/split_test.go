package widget

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func TestSplitPane_ZeroWidth_NoNegative(t *testing.T) {
	s := NewSplitPane(tui.NewStyles(), 0.35)
	s.SetSize(0, 24) // width=0 triggers collapsed (< 80)

	assert.GreaterOrEqual(t, s.LeftWidth(), 0)
	assert.GreaterOrEqual(t, s.RightWidth(), 0)
}

func TestSplitPane_NegativeWidthDefense(t *testing.T) {
	s := NewSplitPane(tui.NewStyles(), 0.35)
	// Force non-collapsed math with a tiny width that would yield negative
	// right width without the max(0,...) floor.
	s.width = 0
	s.collapsed = false

	assert.GreaterOrEqual(t, s.LeftWidth(), 0, "LeftWidth must be >= 0")
	assert.GreaterOrEqual(t, s.RightWidth(), 0, "RightWidth must be >= 0")

	// Also verify with a negative width to be thorough
	s.width = -1
	s.collapsed = false

	assert.GreaterOrEqual(t, s.LeftWidth(), 0, "LeftWidth must be >= 0 for negative width")
	assert.GreaterOrEqual(t, s.RightWidth(), 0, "RightWidth must be >= 0 for negative width")
}
