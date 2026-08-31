package workspace

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// errorNotice is the line the reader gets when a request fails: what was being
// done, then the same sentence the CLI prints for that error rather than the
// SDK's own text. The auth hint is the one thing left out — it tells somebody at
// a shell prompt what to run, and this is read inside a full-screen app. The
// message may be the server's, so the line is sanitized before it is shown.
func errorNotice(what string, err error) string {
	e := output.AsError(err)
	if e.Hint != "" && e.Code != output.CodeAuth {
		return richtext.SanitizeSingleLine(what + ": " + e.Message + " — " + e.Hint)
	}
	return richtext.SanitizeSingleLine(what + ": " + e.Message)
}

const errorViewHint = "esc to dismiss · ctrl+c ctrl+c to quit"

// errorView renders an error inside a bordered box. It is drawn over the screen
// it interrupted, so every line is padded to one width: an overlay's blank cells
// are what keep the content beneath from bleeding through.
func errorView(message string, width int, styles *tui.Styles) string {
	border := lipgloss.NewStyle().Foreground(styles.Theme().Error)
	text := lipgloss.NewStyle().Foreground(styles.Theme().Error).Bold(true)
	hint := lipgloss.NewStyle().Foreground(styles.Theme().Muted)

	inner := min(width-4, 60)
	if inner <= 0 {
		return text.Render("Error: " + message)
	}

	lines := wrapText(message, inner)
	innerWidth := 6
	for _, line := range lines {
		if tui.DisplayWidth(line) > innerWidth {
			innerWidth = tui.DisplayWidth(line)
		}
	}

	hintText := "  " + errorViewHint
	blockWidth := max(innerWidth+4, tui.DisplayWidth(hintText))
	padTo := func(rendered string) string {
		return rendered + strings.Repeat(" ", max(blockWidth-tui.DisplayWidth(rendered), 0))
	}

	var b strings.Builder
	b.WriteString(padTo(border.Render("╭─ Error "+strings.Repeat("─", innerWidth-6)+"╮")) + "\n")
	for _, line := range lines {
		pad := strings.Repeat(" ", innerWidth-tui.DisplayWidth(line))
		b.WriteString(padTo(border.Render("│")+" "+text.Render(line)+pad+" "+border.Render("│")) + "\n")
	}
	b.WriteString(padTo(border.Render("╰"+strings.Repeat("─", innerWidth+2)+"╯")) + "\n")
	b.WriteString(strings.Repeat(" ", blockWidth) + "\n")
	b.WriteString(padTo(hint.Render(hintText)))
	return b.String()
}

// wrapText breaks a line to fit a width, at spaces.
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{s}
	}

	lines := make([]string, 0, 4)
	line := words[0]
	for _, word := range words[1:] {
		if tui.DisplayWidth(line)+1+tui.DisplayWidth(word) > width {
			lines = append(lines, line)
			line = word
		} else {
			line += " " + word
		}
	}
	return append(lines, line)
}
