package views

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

// viewTargetNames maps view targets to human-readable names.
var viewTargetNames = map[workspace.ViewTarget]string{
	workspace.ViewTodos:     "Todos",
	workspace.ViewCampfire:  "Campfire",
	workspace.ViewHey:       "Hey!",
	workspace.ViewCards:     "Card Table",
	workspace.ViewMessages:  "Messages",
	workspace.ViewSearch:    "Search",
	workspace.ViewMyStuff:   "My Stuff",
	workspace.ViewPeople:    "People",
	workspace.ViewDetail:    "Detail",
	workspace.ViewSchedule:  "Schedule",
	workspace.ViewDocsFiles: "Docs & Files",
	workspace.ViewCheckins:  "Check-ins",
	workspace.ViewForwards:  "Email Forwards",
}

// Placeholder is a temporary view for unimplemented screens.
type Placeholder struct {
	session *workspace.Session
	name    string
	width   int
	height  int
}

// NewPlaceholder creates a placeholder view.
func NewPlaceholder(session *workspace.Session, target workspace.ViewTarget) *Placeholder {
	name := viewTargetNames[target]
	if name == "" {
		name = fmt.Sprintf("View %d", target)
	}
	return &Placeholder{
		session: session,
		name:    name,
	}
}

func (v *Placeholder) Title() string                       { return v.name }
func (v *Placeholder) ShortHelp() []key.Binding            { return nil }
func (v *Placeholder) FullHelp() [][]key.Binding           { return nil }
func (v *Placeholder) SetSize(w, h int)                    { v.width = w; v.height = h }
func (v *Placeholder) Init() tea.Cmd                       { return nil }
func (v *Placeholder) Update(tea.Msg) (tea.Model, tea.Cmd) { return v, nil }

func (v *Placeholder) View() string {
	theme := v.session.Styles().Theme()
	style := lipgloss.NewStyle().
		Width(v.width).
		Height(v.height).
		Align(lipgloss.Center, lipgloss.Center).
		Foreground(theme.Muted)
	return style.Render(v.name)
}
