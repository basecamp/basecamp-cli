package workspace

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// View is the interface that all workspace views must implement.
// Decoupled from tea.Model so views return string from View() and
// View (not tea.Model) from Update().
type View interface {
	Init() tea.Cmd
	Update(tea.Msg) (View, tea.Cmd)
	View() string
	Title() string
	ShortHelp() []key.Binding
	FullHelp() [][]key.Binding
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

// FocusedItemScope holds the account/project/recording context of the
// currently focused list item.  Zero-valued fields mean "unknown/same as
// the session scope".
type FocusedItemScope struct {
	AccountID   string
	ProjectID   int64
	RecordingID int64
}

// FocusedRecording is an optional interface for views that can identify the
// scope of the currently focused item. Used by open-in-browser to route
// through the correct account/project.
type FocusedRecording interface {
	FocusedItem() FocusedItemScope
}

// SplitPaneFocuser is an optional interface for views that use a split-pane
// layout with internal tab-cycling. When the sidebar is open, the workspace
// routes tab to the view instead of consuming it for sidebar focus switching.
type SplitPaneFocuser interface {
	HasSplitPane() bool
}
