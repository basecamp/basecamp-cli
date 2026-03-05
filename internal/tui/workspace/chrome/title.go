package chrome

import tea "charm.land/bubbletea/v2"

// WindowTitleMsg requests the workspace to update the window title.
type WindowTitleMsg struct {
	Title string
}

// SetTerminalTitle returns a Cmd that sends a WindowTitleMsg.
// The workspace handles this by setting its windowTitle field,
// which is rendered via View().WindowTitle in v2.
func SetTerminalTitle(title string) tea.Cmd {
	return func() tea.Msg {
		return WindowTitleMsg{Title: title}
	}
}
