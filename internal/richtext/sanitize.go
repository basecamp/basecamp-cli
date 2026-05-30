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
