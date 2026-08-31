package workspace

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// helpBinding is one key and what it does, as the help bar shows it.
type helpBinding struct {
	key  string
	desc string
}

// modifiersLast moves the chorded keys to the end of the bar, keeping the order
// within each group. The single-key bindings are the ones a reader reaches for
// while working, and scattering ctrl+ chords among them pushes those onto a
// second line and makes the whole bar read as a lookup table. Held together at
// the end they are a group you can skip past.
func modifiersLast(bindings []helpBinding) []helpBinding {
	plain := make([]helpBinding, 0, len(bindings))
	chorded := make([]helpBinding, 0, len(bindings))
	for _, binding := range bindings {
		if strings.Contains(binding.key, "+") {
			chorded = append(chorded, binding)
		} else {
			plain = append(plain, binding)
		}
	}
	return append(plain, chorded...)
}

// helpBar is the row of key bindings along the bottom of the screen.
type helpBar struct {
	width    int
	bindings []helpBinding
	styles   *tui.Styles
	hidden   bool
	notice   string
}

func newHelpBar(styles *tui.Styles) helpBar {
	return helpBar{styles: styles}
}

func (h *helpBar) setWidth(width int) {
	h.width = width
}

func (h *helpBar) setBindings(bindings []helpBinding) {
	h.bindings = modifiersLast(bindings)
}

func (h *helpBar) setHidden(hidden bool) {
	h.hidden = hidden
}

// setNotice replaces the bar with one line of its own until the next key press.
// It is where something the reader needs to see but does not need to act on
// goes: a preference that could not be saved, a watch that stopped.
func (h *helpBar) setNotice(notice string) {
	h.notice = notice
}

// height is how many rows the bar takes, which the content area gives up.
func (h helpBar) height() int {
	view := h.view()
	if view == "" {
		return 0
	}
	return strings.Count(view, "\n") + 1
}

func (h helpBar) view() string {
	if h.notice != "" {
		notice := h.styles.Error.Render(h.notice)
		if h.width > 0 {
			return ansi.Wrap(notice, h.width, "")
		}
		return notice
	}
	if h.hidden || len(h.bindings) == 0 {
		return ""
	}

	keyStyle := lipgloss.NewStyle().Foreground(h.styles.Theme().Border).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(h.styles.Theme().Border)
	separator := descStyle.Render(" • ")
	separatorWidth := lipgloss.Width(separator)

	var lines []string
	var line strings.Builder
	lineWidth := 0

	for _, binding := range h.bindings {
		item := keyStyle.Render(binding.key) + " " + descStyle.Render(binding.desc)
		itemWidth := lipgloss.Width(item)

		if h.width > 0 && itemWidth > h.width {
			if lineWidth > 0 {
				lines = append(lines, line.String())
				line.Reset()
			}
			wrapped := strings.Split(ansi.Wrap(item, h.width, ""), "\n")
			lines = append(lines, wrapped[:len(wrapped)-1]...)
			line.WriteString(wrapped[len(wrapped)-1])
			lineWidth = lipgloss.Width(wrapped[len(wrapped)-1])
			continue
		}
		if lineWidth > 0 && h.width > 0 && lineWidth+separatorWidth+itemWidth > h.width {
			lines = append(lines, line.String())
			line.Reset()
			lineWidth = 0
		}
		if lineWidth > 0 {
			line.WriteString(separator)
			lineWidth += separatorWidth
		}
		line.WriteString(item)
		lineWidth += itemWidth
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}

	return strings.Join(lines, "\n")
}
