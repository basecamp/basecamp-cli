package chrome

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// paletteAction mirrors workspace.Action for the chrome layer, avoiding an
// import cycle back to the workspace package. The Palette receives these via
// SetActions rather than reaching into the registry directly.
type paletteAction struct {
	Name        string
	Description string
	Category    string
	Execute     func() tea.Cmd
}

// PaletteCloseMsg is sent when the palette wants to close itself.
type PaletteCloseMsg struct{}

// PaletteExecMsg carries the command returned by the selected action.
type PaletteExecMsg struct {
	Cmd tea.Cmd
}

// Palette is the command palette overlay — a text input with a filtered
// list of actions underneath. It is driven by the workspace and does not
// own the registry directly.
type Palette struct {
	styles *tui.Styles

	input    textinput.Model
	actions  []paletteAction // full set for the current scope
	filtered []paletteAction // subset matching the current query
	cursor   int

	width, height int
}

// NewPalette creates a new command palette component.
func NewPalette(styles *tui.Styles) Palette {
	ti := textinput.New()
	ti.Placeholder = "Type a command..."
	ti.CharLimit = 128
	ti.Prompt = ": "

	return Palette{
		styles: styles,
		input:  ti,
	}
}

// SetActions replaces the full action list (already scope-filtered by the workspace).
func (p *Palette) SetActions(names []string, descriptions []string, categories []string, executors []func() tea.Cmd) {
	p.actions = make([]paletteAction, len(names))
	for i := range names {
		p.actions[i] = paletteAction{
			Name:        names[i],
			Description: descriptions[i],
			Category:    categories[i],
			Execute:     executors[i],
		}
	}
	p.refilter()
}

// Focus activates the text input and resets state for a fresh open.
func (p *Palette) Focus() tea.Cmd {
	p.input.SetValue("")
	p.cursor = 0
	p.refilter()
	return p.input.Focus()
}

// Blur deactivates the text input.
func (p *Palette) Blur() {
	p.input.Blur()
}

// SetSize sets the available dimensions for the overlay.
func (p *Palette) SetSize(width, height int) {
	p.width = width
	p.height = height
	p.input.Width = max(0, width-8) // account for padding + prompt
}

// Update handles key messages while the palette is active.
// Returns a tea.Cmd if the palette produces an action or wants to close.
func (p *Palette) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return p.handleKey(msg)
	}
	return nil
}

func (p *Palette) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		return func() tea.Msg { return PaletteCloseMsg{} }

	case "enter":
		if len(p.filtered) > 0 && p.cursor < len(p.filtered) {
			action := p.filtered[p.cursor]
			cmd := action.Execute()
			return tea.Batch(
				func() tea.Msg { return PaletteCloseMsg{} },
				func() tea.Msg { return PaletteExecMsg{Cmd: cmd} },
			)
		}
		return nil

	case "up", "ctrl+k":
		if p.cursor > 0 {
			p.cursor--
		}
		return nil

	case "down", "ctrl+j":
		if p.cursor < len(p.filtered)-1 {
			p.cursor++
		}
		return nil

	case "ctrl+p":
		// Toggle off — treat as close
		return func() tea.Msg { return PaletteCloseMsg{} }

	default:
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		p.refilter()
		return cmd
	}
}

func (p *Palette) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if q == "" {
		p.filtered = make([]paletteAction, len(p.actions))
		copy(p.filtered, p.actions)
	} else {
		p.filtered = p.filtered[:0]
		for _, a := range p.actions {
			if strings.Contains(strings.ToLower(a.Name), q) ||
				strings.Contains(strings.ToLower(a.Description), q) {
				p.filtered = append(p.filtered, a)
			}
		}
	}
	// Clamp cursor
	if p.cursor >= len(p.filtered) {
		p.cursor = len(p.filtered) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// maxVisibleItems is the maximum number of action rows shown in the palette.
const maxVisibleItems = 12

// View renders the command palette overlay.
func (p Palette) View() string {
	theme := p.styles.Theme()

	// Palette box width: 60 chars or terminal width - 8, whichever is smaller
	boxWidth := 60
	if p.width-8 < boxWidth {
		boxWidth = p.width - 8
	}
	if boxWidth < 30 {
		boxWidth = 30
	}

	// Input line
	inputLine := p.input.View()

	// Separator
	sep := lipgloss.NewStyle().
		Foreground(theme.Border).
		Width(boxWidth - 4). // account for box padding
		Render(strings.Repeat("─", boxWidth-4))

	// Action list — scroll window keeps cursor visible
	var rows []string
	start := 0
	if p.cursor >= maxVisibleItems {
		start = p.cursor - maxVisibleItems + 1
	}
	end := start + maxVisibleItems
	if end > len(p.filtered) {
		end = len(p.filtered)
	}
	visible := p.filtered[start:end]
	for i, a := range visible {
		name := lipgloss.NewStyle().Foreground(theme.Primary).Render(a.Name)
		desc := lipgloss.NewStyle().Foreground(theme.Muted).Render("  " + a.Description)
		line := name + desc

		if i+start == p.cursor {
			line = lipgloss.NewStyle().
				Background(theme.Border).
				Width(boxWidth - 4).
				Render(
					lipgloss.NewStyle().Foreground(theme.Primary).Background(theme.Border).Render(a.Name) +
						lipgloss.NewStyle().Foreground(theme.Muted).Background(theme.Border).Render("  "+a.Description),
				)
		}
		rows = append(rows, line)
	}

	if len(p.filtered) == 0 {
		rows = append(rows, lipgloss.NewStyle().Foreground(theme.Muted).Render("No matching commands"))
	}

	// Footer hint
	count := fmt.Sprintf("%d/%d", len(p.filtered), len(p.actions))
	footer := lipgloss.NewStyle().Foreground(theme.Muted).Render(count)

	// Assemble
	sections := make([]string, 0, 2+len(rows)+2)
	sections = append(sections, inputLine)
	sections = append(sections, sep)
	sections = append(sections, rows...)
	sections = append(sections, sep)
	sections = append(sections, footer)

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(0, 1).
		Width(boxWidth)

	rendered := box.Render(content)

	// Center horizontally
	return lipgloss.NewStyle().
		Width(p.width).
		Align(lipgloss.Center).
		Render(rendered)
}
