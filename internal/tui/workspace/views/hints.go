package views

import "github.com/charmbracelet/bubbles/key"

// filterHints returns the key hints shown when a list filter is active.
func filterHints() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")),
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "results")),
	}
}
