package chrome

import tea "github.com/charmbracelet/bubbletea"

// SetTerminalTitle returns a Cmd that sets the terminal window/tab title
// using Bubble Tea's built-in OSC escape sequence support.
func SetTerminalTitle(title string) tea.Cmd {
	return tea.SetWindowTitle(title)
}
