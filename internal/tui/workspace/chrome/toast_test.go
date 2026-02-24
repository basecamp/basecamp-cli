package chrome

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func TestToast_GenerationPreventsEarlyDismiss(t *testing.T) {
	styles := tui.NewStyles()
	toast := NewToast(styles)
	toast.SetWidth(80)

	// Show first toast — returns tick cmd with generation 1
	toast.Show("First", false)
	firstGen := toast.generation

	// Show second toast — advances generation to 2
	toast.Show("Second", false)
	assert.True(t, toast.Visible())
	assert.Equal(t, "Second", toast.message)

	// Execute the first tick (generation 1) — should NOT dismiss
	toast.Update(toastTickMsg{generation: firstGen})
	assert.True(t, toast.Visible(), "first tick should not dismiss second toast")
	assert.Equal(t, "Second", toast.message)

	// Execute the second tick (current generation) — should dismiss
	toast.Update(toastTickMsg{generation: toast.generation})
	assert.False(t, toast.Visible(), "matching generation tick should dismiss")
}
