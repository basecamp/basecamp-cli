package workspace

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// A toast says what just happened — a todo checked off, a message posted — in
// the top right corner, over whatever is on screen, and takes itself away. It
// belongs to the model rather than to each view because it outlives the thing
// that raised it: a save that navigates away should still say it saved.
//
// What stays an inline notice instead is anything describing the state of the
// screen — a list that stopped following the server, a read that failed and
// wants a key, a confirmation waiting to be answered. Those have to still be
// readable after two seconds.
const toastDuration = 2 * time.Second

type toastKind int

const (
	toastInfo toastKind = iota
	toastError
)

// notifyMsg raises a toast. A view asks for one by returning notify(...) as a
// command, so the request travels the same path as everything else it answers
// with.
type notifyMsg struct {
	text string
	kind toastKind
}

// toastExpiredMsg carries the id of the toast whose time is up, so a second
// toast raised while the first is on screen is not cleared by the first one's
// timer.
type toastExpiredMsg struct {
	id uint64
}

func notify(text string) tea.Cmd {
	return func() tea.Msg { return notifyMsg{text: text} }
}

func notifyError(what string, err error) tea.Cmd {
	return func() tea.Msg { return notifyMsg{text: errorNotice(what, err), kind: toastError} }
}

// showToast puts one on screen and starts its clock.
func (m *model) showToast(msg notifyMsg) tea.Cmd {
	m.toastID++
	m.toast = msg
	id := m.toastID
	return tea.Tick(toastDuration, func(time.Time) tea.Msg { return toastExpiredMsg{id: id} })
}

// toastView is the toast itself, or nothing when none is up.
func (m model) toastView() string {
	if m.toast.text == "" {
		return ""
	}
	theme := m.styles.Theme()
	border := theme.Border
	text := lipgloss.NewStyle().Foreground(theme.Foreground)
	if m.toast.kind == toastError {
		border = theme.Error
		text = lipgloss.NewStyle().Foreground(theme.Error)
	}

	// A toast sits over the content, so it can never be wider than half the
	// screen: the reader is looking at what they were doing, not at this.
	body := truncateToWidth(richtext.SanitizeSingleLine(m.toast.text), max(m.width/2-4, 10))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1).
		Render(text.Render(body))
}
