package richtext

import "strings"

// ShellQuote renders s so a POSIX shell reads it as one literal word:
// unchanged when nothing in it can mean anything to a shell, and otherwise
// wrapped in single quotes with each embedded single quote spliced out and
// back in as:
//
//	'\''
//
// that is: quote, backslash, quote, quote. It is written as an indented
// block on purpose — gofmt reformats doc-comment prose and rewrites that
// sequence into a curly closing quote, which is how the wrong spelling got
// into four files and survived being fixed once (Copilot on #765). A caller
// copying it from prose would get a malformed form.
//
// It is an encoding applied to the whole value, not a metacharacter list.
// Hints and breadcrumbs are text a person pastes into a shell, and they
// interpolate values from configuration files and from the API — neither of
// which is held to whatever check applied when the value was first created.
// Escaping cases one at a time is how quoting bugs recur.
//
// internal/auth, internal/config and internal/commands each carried a copy
// of this, written before there was a shared home for it. They are gone:
// this is the only one, and the only one new code should use. The config
// copy quoted unconditionally, so paths that need no quoting now appear
// bare in its warnings — the same shell word, spelled shorter.
func ShellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, shellActive) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellActive reports whether r can mean anything to a POSIX shell outside
// quotes; letters, digits and a few inert punctuation marks cannot.
func shellActive(r rune) bool {
	inert := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:@%+=-", r)
	return !inert
}
