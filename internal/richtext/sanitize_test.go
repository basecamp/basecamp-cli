package richtext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeTerminalStripsESCSequences(t *testing.T) {
	assert.Equal(t, "red", SanitizeTerminal("\x1b[31mred\x1b[0m"))
	assert.Equal(t, "click", SanitizeTerminal("\x1b]8;;http://evil\x07click\x1b]8;;\x07"))
}

func TestSanitizeTerminalStripsC1Controls(t *testing.T) {
	// UTF-8-encoded Unicode C1 controls survive ansi.Strip but are executed
	// by C1-honoring terminals, so the sanitizer must drop them.
	assert.Equal(t, "31mred0m", SanitizeTerminal("\u009b31mred\u009b0m"), "U+009B CSI")
	assert.Equal(t, "0;evil", SanitizeTerminal("\u009d0;evil\a"), "U+009D OSC, BEL-terminated")
	assert.Equal(t, "payload", SanitizeTerminal("\u0090payload\u009c"), "U+0090 DCS / U+009C ST")
	assert.Equal(t, "ab", SanitizeTerminal("a\u007fb"), "DEL")
}

func TestSanitizeTerminalStripsC0Controls(t *testing.T) {
	assert.Equal(t, "ab", SanitizeTerminal("a\rb"))
	assert.Equal(t, "ab", SanitizeTerminal("a\x00\x07\x08b"))
}

func TestSanitizeTerminalPreservesNewlineTabAndText(t *testing.T) {
	assert.Equal(t, "a\nb\tc", SanitizeTerminal("a\nb\tc"))
	assert.Equal(t, "héllo — wörld", SanitizeTerminal("héllo — wörld"))
	assert.Equal(t, "", SanitizeTerminal(""))
}

func TestSanitizeSingleLineCollapsesWhitespace(t *testing.T) {
	// Bare CR between words becomes a separator, not a glue.
	assert.Equal(t, "a b", SanitizeSingleLine("a\rb"))
	assert.Equal(t, "a b", SanitizeSingleLine("a\r\nb"))
	// Embedded newlines and tabs can no longer break a single line.
	assert.Equal(t, "a b c", SanitizeSingleLine("a\nb\tc"))
	assert.Equal(t, "one two three", SanitizeSingleLine("  one   two\tthree  "))
}

func TestSanitizeSingleLineStripsEscapesAndControls(t *testing.T) {
	out := SanitizeSingleLine("\x1b[31mred\x1b[0m\ntext")
	assert.Equal(t, "red text", out)
	assert.Equal(t, "31mred", SanitizeSingleLine("\u009b31mred"))
	// Combined: CR-separated words + newline + tab + C1/ESC sequences.
	out = SanitizeSingleLine("evil\u009b31m\rname\ttitle\n\x1b]8;;u\x07link")
	assert.NotContains(t, out, "\n")
	assert.NotContains(t, out, "\r")
	assert.NotContains(t, out, "\t")
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\u009b")
}

func TestSanitizeSingleLineEmptyForAllControl(t *testing.T) {
	assert.Equal(t, "", SanitizeSingleLine(""))
	assert.Equal(t, "", SanitizeSingleLine("\r\n\t   "))
	assert.Equal(t, "", SanitizeSingleLine("\x1b[0m\u009b"))
}
