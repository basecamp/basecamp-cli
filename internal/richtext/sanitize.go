package richtext

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// SanitizeTerminal removes terminal escape sequences and control characters
// from API-controlled text before it reaches a terminal sink.
//
// ansi.Strip alone is not enough: it only removes ESC-prefixed (7-bit)
// sequences and preserves UTF-8-encoded Unicode C1 controls — U+009B (CSI),
// U+009D (OSC), U+0090 (DCS), and the rest of U+0080–U+009F — plus DEL and
// bare C0 bytes like BEL and CR. Terminals that honor 8-bit C1 controls
// execute those directly, so a name containing U+009B (CSI) followed by
// "31m" would still restyle or inject after ansi.Strip. After stripping
// ESC sequences,
// this drops every remaining C0 control (except newline and tab), DEL, and
// the C1 range.
func SanitizeTerminal(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		default:
			return r
		}
	}, s)
}

// SanitizeSingleLine sanitizes API-controlled text for a single-line terminal
// sink. It normalizes CR/CRLF to newlines, strips terminal escape sequences and
// control characters via SanitizeTerminal, then collapses all remaining
// whitespace (newlines, tabs, runs of spaces) to single spaces so the value
// occupies exactly one line. Bare CR between words becomes a space separator
// rather than gluing words together, and embedded newlines/tabs can no longer
// break a single-line layout. This mirrors the canonical ordering used by
// output.sanitizeText with singleLine=true. All-whitespace or all-control input
// collapses to the empty string.
func SanitizeSingleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = SanitizeTerminal(s)
	return strings.Join(strings.Fields(s), " ")
}
