package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func TestBoostPicker_ViewCopy(t *testing.T) {
	p := NewBoostPicker(tui.NewStyles())
	p.SetSize(80, 40)
	view := p.View()

	assert.Contains(t, view, "Give a Boost!")
	assert.Contains(t, view, "Or write a short note (16 chars max):")
	assert.NotContains(t, view, "Boost this!")
	assert.NotContains(t, view, "Type an emoji")
}

func TestBoostPicker_Placeholder(t *testing.T) {
	p := NewBoostPicker(tui.NewStyles())
	p.SetSize(80, 40)
	view := p.View()

	assert.Contains(t, view, "short note or emoji")
}
