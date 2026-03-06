package chrome

import (
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// Ticker renders a single-line ambient display of campfire activity.
type Ticker struct {
	styles  *tui.Styles
	width   int
	entries []data.BonfireDigestEntry
	flash   map[string]time.Time // room key -> flash start time
}

// NewTicker creates a new Ticker.
func NewTicker(styles *tui.Styles) Ticker {
	return Ticker{
		styles: styles,
		flash:  make(map[string]time.Time),
	}
}

// SetWidth sets the ticker width.
func (t *Ticker) SetWidth(w int) {
	t.width = w
}

// SetEntries updates the digest entries.
func (t *Ticker) SetEntries(entries []data.BonfireDigestEntry) {
	t.entries = entries
}

// Flash marks a room for brief highlight.
func (t *Ticker) Flash(roomKey string) {
	t.flash[roomKey] = time.Now()
}

// Entries returns the current digest entries.
func (t Ticker) Entries() []data.BonfireDigestEntry {
	return t.entries
}

// Active returns true when there are entries to display.
func (t Ticker) Active() bool {
	return len(t.entries) > 0
}

// View renders the ticker line.
func (t Ticker) View() string {
	if len(t.entries) == 0 {
		return ""
	}

	theme := t.styles.Theme()
	now := time.Now()
	var parts []string

	for _, entry := range t.entries {
		if entry.LastMessage == "" {
			continue
		}

		// Room badge: 2-char abbreviation
		badge := abbreviate(entry.RoomName, 2)
		colorIdx := entry.Color(len(theme.RoomColors))
		var badgeStyle lipgloss.Style
		if colorIdx < len(theme.RoomColors) {
			badgeStyle = lipgloss.NewStyle().Bold(true).Foreground(theme.RoomColors[colorIdx])
		} else {
			badgeStyle = lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
		}

		// Check for flash
		key := entry.Key()
		msgStyle := lipgloss.NewStyle().Foreground(theme.Secondary)
		if flashTime, ok := t.flash[key]; ok {
			if now.Sub(flashTime) < 2*time.Second {
				msgStyle = lipgloss.NewStyle().Bold(true).Foreground(theme.Foreground)
			} else {
				delete(t.flash, key)
			}
		}

		// Format: "BA Author: message"
		msg := entry.LastMessage
		if entry.LastAuthor != "" {
			msg = entry.LastAuthor + ": " + msg
		}

		part := badgeStyle.Render(badge) + " " + msgStyle.Render(msg)
		parts = append(parts, part)
	}

	if len(parts) == 0 {
		return ""
	}

	line := strings.Join(parts, " \u2502 ")

	// Truncate to width
	if t.width > 0 && utf8.RuneCountInString(line) > t.width {
		runes := []rune(line)
		line = string(runes[:t.width-1]) + "\u2026"
	}

	return line
}

// abbreviate returns the first n uppercase characters of a name.
func abbreviate(name string, n int) string {
	runes := []rune(strings.ToUpper(name))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n])
}
