package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Wordmark is a braille-art rendering of the Basecamp mountain logo.
const Wordmark = "" +
	"⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢀⣠⣤⣶⣶⣶⣶⣶⣶⣦⣤⣀\n" +
	"⠀⠀⠀⠀⠀⠀⠀⢀⣴⣾⣿⣿⣿⠿⠿⠛⠛⠛⠻⠿⣿⣿⣿⣦⣀\n" +
	"⠀⠀⠀⠀⠀⢀⣴⣿⣿⡿⠛⠁⠀⠀⠀⠀⠀⠀⠀⠀⠀⠙⠻⣿⣿⣦⡀\n" +
	"⠀⠀⠀⠀⣴⣿⣿⡿⠋⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠘⢿⣿⣿⣄\n" +
	"⠀⠀⢀⣼⣿⣿⠏⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣠⣤⡀⠀⠀⠀⠀⢻⣿⣿⣆\n" +
	"⠀⢀⣾⣿⣿⠃⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⣼⣿⣿⠃⠀⠀⠀⠀⠀⢻⣿⣿⡄\n" +
	"⠀⣼⣿⣿⠃⠀⠀⣠⣶⣿⣷⣦⣄⠀⠀⢀⣼⣿⣿⠃⠀⠀⠀⠀⠀⠀⠀⢿⣿⣿⡀\n" +
	"⢸⣿⣿⠇⠀⢠⣾⣿⡿⠛⠻⣿⣿⣷⣤⣾⣿⡿⠃⠀⠀⠀⠀⠀⠀⠀⠀⠘⣿⣿⣇\n" +
	"⠈⠉⠉⠀⢠⣿⣿⡟⠁⠀⠀⠈⠻⣿⣿⣿⠟⠁⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢻⣿⣿\n" +
	"⠀⠀⠀⢠⣿⣿⡟⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢸⣿⣿⡇\n" +
	"⠀⠀⠀⢻⣿⣿⣦⡀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢀⣼⣿⣿⠇\n" +
	"⠀⠀⠀⠀⠙⠿⣿⣿⣷⣤⣀⡀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⢀⣀⣤⣶⣿⣿⡿⠋\n" +
	"⠀⠀⠀⠀⠀⠀⠈⠛⠿⣿⣿⣿⣿⣶⣶⣶⣶⣶⣶⣶⣶⣶⣿⣿⣿⣿⡿⠟⠉\n" +
	"⠀⠀⠀⠀⠀⠀⠀⠀⠀⠀⠉⠙⠛⠛⠿⠿⠿⠿⠿⠿⠟⠛⠛⠉⠉"

// WordmarkLines is the number of lines in the Wordmark.
const WordmarkLines = 14

// WordmarkWidth is the display width (rune count of widest line) of the Wordmark.
const WordmarkWidth = 32

const (
	brandText     = "Basecamp"
	brandTextLine = 6 // line index where "Basecamp" appears to the right
)

// BrandColor is the fixed Basecamp brand yellow, independent of theme.
var BrandColor = lipgloss.Color("#e8a217")

// RenderWordmark renders the wordmark in Basecamp brand yellow with
// "Basecamp" to the right of the logo. Returns plain text with NoColorTheme.
func RenderWordmark(theme Theme) string {
	style := lipgloss.NewStyle()
	if _, noColor := theme.Primary.(lipgloss.NoColor); !noColor {
		style = style.Foreground(BrandColor)
	}

	var b strings.Builder
	for i, line := range strings.Split(Wordmark, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(style.Render(line))
		if i == brandTextLine {
			b.WriteString("   ")
			b.WriteString(style.Render(brandText))
		}
	}
	return b.String()
}
