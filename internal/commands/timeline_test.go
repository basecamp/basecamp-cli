package commands

import (
	"strings"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// TestFormatEventSanitizesSingleLine verifies that API-controlled fields
// rendered into the single-line alt-screen watch event string are collapsed to
// one line: bare CR between words becomes a separator (words are not glued),
// and embedded newlines/tabs/escape sequences cannot break the layout or inject
// into the terminal.
func TestFormatEventSanitizesSingleLine(t *testing.T) {
	e := basecamp.TimelineEvent{
		Creator: &basecamp.Person{Name: "Ann\rBeth"},
		Action:  "com\nmented",
		Title:   "hello\tworld\x1b[31m\r\nagain\u009b0m",
	}

	// Strip lipgloss's own styling so we assert on the API-controlled content.
	got := ansi.Strip(formatEvent(e))

	assert.NotContains(t, got, "\n")
	assert.NotContains(t, got, "\r")
	assert.NotContains(t, got, "\t")
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\u009b")

	// Words remain space-separated rather than glued together.
	assert.Contains(t, got, "Ann Beth")
	assert.Contains(t, got, "com mented")
	assert.Contains(t, got, "hello world")
	assert.NotContains(t, got, "AnnBeth")
	assert.NotContains(t, got, "commented")
}

// TestFormatEventEmptyFieldsFallBack verifies the empty-check fallbacks still
// fire when a field sanitizes down to the empty string (all whitespace/control).
func TestFormatEventEmptyFieldsFallBack(t *testing.T) {
	e := basecamp.TimelineEvent{
		Creator:        &basecamp.Person{Name: "\x1b[0m\r\n"},
		Action:         "\t \r ",
		Title:          "\x1b[31m",
		SummaryExcerpt: "fell\rback",
	}

	got := ansi.Strip(formatEvent(e))

	assert.Contains(t, got, "Someone")
	assert.Contains(t, got, "updated")
	// Title was empty, so SummaryExcerpt is used and also single-lined.
	assert.Contains(t, got, "fell back")
	assert.False(t, strings.Contains(got, "fellback"))
}

// TestWatchLabelSanitizesName verifies a normal name is sanitized for the
// single-line watch-status sink and kept when non-empty.
func TestWatchLabelSanitizesName(t *testing.T) {
	got := watchLabel("activity for %s", "Ann\x1b[31m Beth\r", "12345")
	assert.Equal(t, "activity for Ann Beth", got)
	assert.NotContains(t, got, "\x1b")
}

// TestWatchLabelFallsBackToID verifies that an all-escape name — which collapses
// to "" after sanitization — falls back to the already-resolved ID so the label
// never renders with a blank trailing value ("activity for ").
func TestWatchLabelFallsBackToID(t *testing.T) {
	allEscape := "\x1b[2J\x1b]0;x\x07\x08\x7f"

	person := watchLabel("activity for %s", allEscape, "12345")
	assert.Equal(t, "activity for 12345", person)
	assert.NotContains(t, person, "\x1b")

	project := watchLabel("activity in %s", allEscape, "67890")
	assert.Equal(t, "activity in 67890", project)
}
