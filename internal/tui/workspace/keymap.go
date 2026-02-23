package workspace

import (
	"encoding/json"
	"os"
	"reflect"

	"github.com/charmbracelet/bubbles/key"
)

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
	Activity      key.Binding
	Sidebar       key.Binding
	SidebarFocus  key.Binding
	Refresh       key.Binding
	Open          key.Binding
	Jump          key.Binding
	Metrics       key.Binding
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
			key.WithKeys("ctrl+y"),
			key.WithHelp("ctrl+y", "hey! inbox"),
		),
		MyStuff: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "my stuff"),
		),
		Activity: key.NewBinding(
			key.WithKeys("ctrl+t"),
			key.WithHelp("ctrl+t", "activity"),
		),
		Sidebar: key.NewBinding(
			key.WithKeys("ctrl+b"),
			key.WithHelp("ctrl+b", "sidebar"),
		),
		SidebarFocus: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch panel"),
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
		Metrics: key.NewBinding(
			key.WithKeys("`"),
			key.WithHelp("`", "metrics"),
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
		{k.AccountSwitch, k.Hey, k.MyStuff, k.Activity},
		{k.Help, k.Refresh, k.Open, k.Jump, k.Sidebar, k.Metrics},
	}
}

// actionFieldMap maps action names (from keybindings.json) to GlobalKeyMap field names.
var actionFieldMap = map[string]string{
	"quit":           "Quit",
	"help":           "Help",
	"back":           "Back",
	"search":         "Search",
	"palette":        "Palette",
	"account_switch": "AccountSwitch",
	"hey":            "Hey",
	"my_stuff":       "MyStuff",
	"activity":       "Activity",
	"sidebar":        "Sidebar",
	"sidebar_focus":  "SidebarFocus",
	"refresh":        "Refresh",
	"open":           "Open",
	"jump":           "Jump",
	"metrics":        "Metrics",
}

// LoadKeyOverrides reads keybinding overrides from a JSON file.
// Returns an empty map (not an error) if the file doesn't exist.
func LoadKeyOverrides(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var overrides map[string]string
	if err := json.Unmarshal(data, &overrides); err != nil {
		return nil, err
	}
	return overrides, nil
}

// ApplyOverrides remaps keybindings in km according to the overrides map.
// Keys are action names (e.g. "hey"), values are key strings (e.g. "ctrl+h").
// Unknown actions are silently ignored.
func ApplyOverrides(km *GlobalKeyMap, overrides map[string]string) {
	v := reflect.ValueOf(km).Elem()
	for action, keyStr := range overrides {
		fieldName, ok := actionFieldMap[action]
		if !ok {
			continue
		}
		field := v.FieldByName(fieldName)
		if !field.IsValid() {
			continue
		}
		binding := field.Interface().(key.Binding)
		helpInfo := binding.Help()
		field.Set(reflect.ValueOf(key.NewBinding(
			key.WithKeys(keyStr),
			key.WithHelp(keyStr, helpInfo.Desc),
		)))
	}
}
