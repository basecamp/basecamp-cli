package chrome

import (
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func TestBreadcrumb_LongName_TwoSegments_Truncated(t *testing.T) {
	b := NewBreadcrumb(tui.NewStyles())
	b.SetWidth(30)
	b.SetCrumbs([]string{"Home", "This Is An Extremely Long Project Name That Should Be Truncated"})

	view := b.View()
	w := lipgloss.Width(view)
	if w > 30 {
		t.Errorf("expected view width <= 30, got %d", w)
	}
	if w == 0 {
		t.Error("expected non-empty view")
	}
}

func TestBreadcrumb_ShortName_TwoSegments_NotTruncated(t *testing.T) {
	b := NewBreadcrumb(tui.NewStyles())
	b.SetWidth(40)
	b.SetCrumbs([]string{"Home", "Todos"})

	view := b.View()
	w := lipgloss.Width(view)
	// "1:Home > 2:Todos" is ~16 chars, should fit easily in 40
	if w > 40 {
		t.Errorf("expected view width <= 40, got %d", w)
	}
	// Should not contain truncation ellipsis
	if containsEllipsis(view) {
		t.Error("short breadcrumb should not be truncated")
	}
}

func TestBreadcrumb_SingleSegment_Truncated(t *testing.T) {
	b := NewBreadcrumb(tui.NewStyles())
	b.SetWidth(15)
	b.SetCrumbs([]string{"This Is A Very Long Single Segment Name"})

	view := b.View()
	w := lipgloss.Width(view)
	if w > 15 {
		t.Errorf("expected view width <= 15, got %d", w)
	}
	if w == 0 {
		t.Error("expected non-empty view")
	}
}

func TestBreadcrumb_ThreeSegments_StillTruncated(t *testing.T) {
	b := NewBreadcrumb(tui.NewStyles())
	b.SetWidth(20)
	b.SetCrumbs([]string{"Home", "Project", "This Is An Extremely Long Name That Should Be Truncated Even After Ellipsis"})

	view := b.View()
	w := lipgloss.Width(view)
	if w > 20 {
		t.Errorf("expected view width <= 20, got %d", w)
	}
	if w == 0 {
		t.Error("expected non-empty view")
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"fits", "Hello", 10, "Hello"},
		{"exact", "Hello", 5, "Hello"},
		{"truncated", "Hello World", 5, "Hello…"},
		{"zero", "Hello", 0, "…"},
		{"negative", "Hello", -1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateText(tt.input, tt.max)
			if got != tt.expected {
				t.Errorf("truncateText(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.expected)
			}
		})
	}
}

func containsEllipsis(s string) bool {
	for _, r := range s {
		if r == '…' {
			return true
		}
	}
	return false
}
