package chrome

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// ToastDuration is how long a toast remains visible.
const ToastDuration = 3 * time.Second

// toastTickMsg is the internal tick for dismissing toasts.
type toastTickMsg struct{}

// Toast renders ephemeral confirmation messages.
type Toast struct {
	styles  *tui.Styles
	width   int
	message string
	isError bool
	visible bool
}

// NewToast creates a new toast component.
func NewToast(styles *tui.Styles) Toast {
	return Toast{styles: styles}
}

// Show displays a toast message.
func (t *Toast) Show(message string, isError bool) tea.Cmd {
	t.message = message
	t.isError = isError
	t.visible = true
	return tea.Tick(ToastDuration, func(time.Time) tea.Msg {
		return toastTickMsg{}
	})
}

// SetWidth sets the available width.
func (t *Toast) SetWidth(w int) {
	t.width = w
}

// Visible returns whether the toast is currently displayed.
func (t *Toast) Visible() bool {
	return t.visible
}

// Update handles toast tick messages.
func (t *Toast) Update(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(toastTickMsg); ok {
		t.visible = false
		t.message = ""
	}
	return nil
}

// View renders the toast.
func (t Toast) View() string {
	if !t.visible || t.message == "" {
		return ""
	}

	theme := t.styles.Theme()
	fg := theme.Success
	if t.isError {
		fg = theme.Error
	}

	style := lipgloss.NewStyle().
		Foreground(fg).
		Align(lipgloss.Center).
		Width(t.width)

	return style.Render(t.message)
}
