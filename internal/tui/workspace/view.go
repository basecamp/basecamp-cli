package workspace

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// View is the interface that all workspace views must implement.
type View interface {
	tea.Model

	// Title returns the breadcrumb segment for this view.
	Title() string

	// ShortHelp returns key bindings shown in the status bar.
	ShortHelp() []key.Binding

	// FullHelp returns all key bindings for the help overlay.
	FullHelp() [][]key.Binding

	// SetSize updates the view's available dimensions.
	SetSize(width, height int)
}

// InputCapturer is an optional interface views can implement to signal
// they are in text input mode. When InputActive returns true, the
// workspace will skip global single-key bindings (q, r, 1-9, etc.)
// and forward all keys directly to the view.
type InputCapturer interface {
	InputActive() bool
}

// ModalActive is an optional interface views can implement to signal
// they have an active modal state (e.g., cards move mode, search results
// focus). When ModalActive returns true, Esc is forwarded to the view
// instead of triggering global back navigation.
type ModalActive interface {
	IsModal() bool
}

// Filterable is an optional interface for views with lists that support
// interactive filtering. When the workspace receives "/" it calls StartFilter
// on the current view instead of navigating to global search.
type Filterable interface {
	StartFilter()
}

// SplitPaneFocuser is an optional interface for views that use a split-pane
// layout with internal tab-cycling. When the sidebar is open, the workspace
// routes tab to the view instead of consuming it for sidebar focus switching.
type SplitPaneFocuser interface {
	HasSplitPane() bool
}
