package workspace

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Default emojis from Basecamp
var defaultEmojis = []string{"👏", "👍", "🙌", "💪", "🤘", "✊", "✨", "❤️", "💯", "🎉", "🤩", "🥳", "😊", "😀", "😂", "😅", "😎", "😉", "😜", "😬", "😮", "😳", "🤔", "😒", "😢", "😭", "😱", "👀", "🙏", "💩", "👎", "✌️", "👈", "👆", "✋", "👋", "☀️", "🌙", "💥", "🔥", "🎂", "🍴", "💰", "🥇", "🚨", "💡", "🛠", "📈", "✅", "📢"}

type BoostSelectedMsg struct {
	Emoji string
}

type BoostPicker struct {
	styles    *tui.Styles
	textInput textinput.Model
	width     int
	height    int
	focused   bool

	// Simple selection mechanism
	cursor int
}

func NewBoostPicker(styles *tui.Styles) *BoostPicker {
	ti := textinput.New()
	ti.Placeholder = "A short note or emoji…"
	ti.CharLimit = 16
	ti.SetWidth(30)

	return &BoostPicker{
		styles:    styles,
		textInput: ti,
		cursor:    0,
	}
}

func (p *BoostPicker) Focus() {
	p.focused = true
	p.textInput.Focus()
}

func (p *BoostPicker) Blur() {
	p.focused = false
	p.textInput.Blur()
}

func (p *BoostPicker) SetSize(width, height int) {
	p.width = width
	p.height = height
}

func (p *BoostPicker) Update(msg tea.Msg) (*BoostPicker, tea.Cmd) {
	if !p.focused {
		return p, nil
	}

	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.Code { //nolint:exhaustive // partial key handler
		case tea.KeyEnter:
			val := strings.TrimSpace(p.textInput.Value())
			if val == "" {
				val = defaultEmojis[p.cursor]
			}
			return p, func() tea.Msg {
				return BoostSelectedMsg{Emoji: val}
			}
		case tea.KeyLeft:
			if p.textInput.Value() == "" {
				if p.cursor > 0 {
					p.cursor--
				}
				return p, nil
			}
		case tea.KeyRight:
			if p.textInput.Value() == "" {
				if p.cursor < len(defaultEmojis)-1 {
					p.cursor++
				}
				return p, nil
			}
		case tea.KeyUp:
			if p.textInput.Value() == "" {
				if p.cursor-10 >= 0 {
					p.cursor -= 10
				}
				return p, nil
			}
		case tea.KeyDown:
			if p.textInput.Value() == "" {
				if p.cursor+10 < len(defaultEmojis) {
					p.cursor += 10
				}
				return p, nil
			}
		}
	}

	p.textInput, cmd = p.textInput.Update(msg)
	return p, cmd
}

func (p *BoostPicker) View() string {
	theme := p.styles.Theme()
	bStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("Give a Boost!"))
	b.WriteString("\n\n")

	// Render a grid of emojis
	for i, e := range defaultEmojis {
		style := lipgloss.NewStyle().Padding(0, 1)
		if i == p.cursor && p.textInput.Value() == "" {
			style = style.Background(theme.Primary).Foreground(theme.Background)
		}
		b.WriteString(style.Render(e))
		if (i+1)%10 == 0 {
			b.WriteString("\n")
		}
	}

	b.WriteString("\n\nOr write a short note (16 chars max):\n")
	b.WriteString(p.textInput.View())
	b.WriteString("\n\n(Enter to send, Esc to cancel)")

	return bStyle.Render(b.String())
}
