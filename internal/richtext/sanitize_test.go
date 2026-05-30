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
