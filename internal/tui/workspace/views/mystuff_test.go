package views

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func TestMyStuff_ShortID_NoPanic(t *testing.T) {
	styles := tui.NewStyles()
	list := widget.NewList(styles)
	list.SetSize(80, 20)
	list.SetFocused(true)

	// An item with ID shorter than any prefix must not panic
	list.SetItems([]widget.ListItem{
		{ID: "x", Title: "Short"},
	})

	v := &MyStuff{
		styles:            styles,
		list:              list,
		recordingProjects: make(map[string]int64),
	}

	cmd := v.openSelected()
	assert.Nil(t, cmd, "short ID should not panic and should return nil")
}
