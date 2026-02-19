package workspace

import "github.com/charmbracelet/bubbles/key"

// GlobalKeyMap defines keybindings that work in every context.
type GlobalKeyMap struct {
	Quit          key.Binding
	Help          key.Binding
	Back          key.Binding
	Search        key.Binding
	Palette       key.Binding
	AccountSwitch key.Binding
	Hey           key.Binding
	MyStuff       key.Binding
	Refresh       key.Binding
	Open          key.Binding
	Jump          key.Binding
}

// DefaultGlobalKeyMap returns the default global keybindings.
func DefaultGlobalKeyMap() GlobalKeyMap {
	return GlobalKeyMap{
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc", "backspace"),
			key.WithHelp("esc", "back"),
		),
		Search: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter/search"),
		),
		Palette: key.NewBinding(
			key.WithKeys("ctrl+p"),
			key.WithHelp("ctrl+p", "command palette"),
		),
		AccountSwitch: key.NewBinding(
			key.WithKeys("ctrl+a"),
			key.WithHelp("ctrl+a", "switch account"),
		),
		Hey: key.NewBinding(
			key.WithKeys("ctrl+h"),
			key.WithHelp("ctrl+h", "hey! inbox"),
		),
		MyStuff: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "my stuff"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Open: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open in browser"),
		),
		Jump: key.NewBinding(
			key.WithKeys("ctrl+j"),
			key.WithHelp("ctrl+j", "jump to"),
		),
	}
}

// ListKeyMap defines keybindings for list navigation.
type ListKeyMap struct {
	Up       key.Binding
	Down     key.Binding
	Top      key.Binding
	Bottom   key.Binding
	PageDown key.Binding
	PageUp   key.Binding
	Open     key.Binding
	Filter   key.Binding
}

// DefaultListKeyMap returns the default list navigation keybindings.
func DefaultListKeyMap() ListKeyMap {
	return ListKeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("j/k", "navigate"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("j/k", "navigate"),
		),
		Top: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G"),
			key.WithHelp("G", "bottom"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "page down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("ctrl+u", "page up"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open"),
		),
		Filter: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "filter"),
		),
	}
}

// ShortHelp returns the global key bindings for the status bar.
func (k GlobalKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Back, k.Palette, k.Quit}
}

// FullHelp returns all global key bindings for the help overlay.
func (k GlobalKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Back, k.Quit},
		{k.Search, k.Palette},
		{k.AccountSwitch, k.Hey, k.MyStuff},
		{k.Help, k.Refresh, k.Open, k.Jump},
	}
}
