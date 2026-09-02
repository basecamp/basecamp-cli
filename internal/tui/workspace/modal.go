package workspace

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modal is what is open over a screen: a form or a confirmation that holds every
// key while it is up. Only one is ever open, so the model keeps one of these
// rather than a field per kind, and the questions the frame asks — where the
// keys go, what is on screen, what the help bar says — have one answer each
// instead of one per form.
//
// This is HEY's pattern. A form is not a place: pushing one onto the nav stack
// puts "Edit Teams" in the breadcrumb trail, as though the reader had traveled
// somewhere, and hides the folder they are editing while they edit it. A modal
// leaves it on screen around the border.
type modal interface {
	// Init returns the command to run when the modal opens.
	Init() tea.Cmd

	// HandleKey routes one key press and answers whether the modal stays open.
	// The closing is the model's to do, so esc and a committed form are the same
	// thing here: a modal that is finished says so and lets go.
	HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool)

	// Update routes what is not a key press — cursor blinks, the answers to its
	// own writes — and answers whether it took it.
	Update(msg tea.Msg) (tea.Cmd, bool)

	// Title is the name across the top of the frame.
	Title() string

	// View renders what goes inside the frame.
	View() string

	HelpBindings() []helpBinding
	Resize(width, height int)
}

// A modal's border takes a column on each side and its padding two more, so a
// frame spends four columns on chrome; the border and the title's rule spend
// four rows.
const (
	modalChromeWidth = 4
	modalChromeRows  = 4

	// The share of the frame a modal may take. A modal that fills the screen is
	// a screen, and the point of one is seeing what it is over.
	modalWidthNumerator   = 3
	modalWidthDenominator = 5
)

func modalWidth(width int) int {
	return max(width*modalWidthNumerator/modalWidthDenominator, minModalWidth)
}

const minModalWidth = 24

// slimModal is implemented by a modal whose content has a width of its own —
// a card about one person is a name and a couple of lines under it, and a frame
// three fifths of the terminal wide around that is mostly empty frame.
//
// The share above is still the ceiling: this asks for less, never for more.
type slimModal interface {
	Widest() int
}

// modalWidthFor is how wide the frame around one modal is drawn: the share of
// the terminal it may take, narrowed to what the modal asked for.
func modalWidthFor(open modal, width int) int {
	room := modalWidth(width)
	slim, ok := open.(slimModal)
	if !ok {
		return room
	}
	return max(min(room, slim.Widest()+modalChromeWidth), minModalWidth)
}

// openModal puts a modal up. Focus goes with it: whatever the screen underneath
// was doing, the reader is doing this now.
func (m *model) openModal(open modal) tea.Cmd {
	m.sidebar.leave()
	m.modal = open
	m.relayout()
	return open.Init()
}

// closeModal takes it down and gives the screen its keys back.
func (m *model) closeModal() {
	m.modal = nil
	m.relayout()
}

// modalFrame is the box a modal is drawn in: its name, a rule, and its content.
func (m model) modalFrame() string {
	if m.modal == nil {
		return ""
	}
	theme := m.ctx.Styles().Theme()
	inner := max(modalWidthFor(m.modal, m.width)-modalChromeWidth, 1)

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).
		Render(truncateToWidth(m.modal.Title(), inner)))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Border).Render(strings.Repeat("─", inner)))
	b.WriteString("\n")
	b.WriteString(m.modal.View())

	// Width is the whole block's, border and padding included, so it is the
	// content width plus the four columns of chrome around it.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(0, 1).
		Width(inner + modalChromeWidth).
		Render(b.String())
}

// withModal draws the modal over everything else, centered, so the screen it
// interrupted stays on screen around its border.
func (m model) withModal(rendered string) string {
	framed := m.modalFrame()
	if framed == "" {
		return rendered
	}
	return overlayCentered(rendered, framed, m.width, lipgloss.Height(rendered))
}
