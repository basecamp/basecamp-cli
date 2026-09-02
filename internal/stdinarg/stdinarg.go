// Package stdinarg carries the shared vocabulary for "-" (read from stdin)
// argument handling: the cobra annotation that marks where a command accepts
// "-", and pipe detection for deciding whether a stray "-" is ambiguous.
//
// It is a leaf package because both internal/commands (which resolves "-" and
// installs the guard) and internal/cli (which surfaces the annotation in agent
// help) need the same annotation key, and cli already depends on commands.
package stdinarg

import (
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
)

// AnnotationAllowDash is the cmd.Annotations key marking where a command
// accepts "-" as "read from stdin". The value is a space-separated list of
// tokens: "arg:0" (exact positional index), "arg:1+" (that index and beyond),
// "flag:data" (the --data flag). Everything not listed is guarded: a literal
// "-" there combined with piped stdin is rejected as ambiguous.
const AnnotationAllowDash = "allow_dash"

// Allow is the parsed form of an AnnotationAllowDash value.
type Allow struct {
	args     map[int]bool
	argsFrom int // "arg:N+" allows every index >= argsFrom; -1 when absent
	flags    map[string]bool
}

// ParseAllow parses a space-separated token list ("arg:0 arg:1+ flag:data")
// into an Allow. Unrecognized tokens are ignored rather than failing: the
// annotation is authored in-repo and covered by tests, so a typo shows up as
// a guarded (rejected) input, not a silent bypass.
func ParseAllow(s string) Allow {
	allow := Allow{argsFrom: -1}
	for _, token := range strings.Fields(s) {
		switch {
		case strings.HasPrefix(token, "arg:"):
			spec := strings.TrimPrefix(token, "arg:")
			open := strings.HasSuffix(spec, "+")
			if n, err := strconv.Atoi(strings.TrimSuffix(spec, "+")); err == nil {
				if open {
					if allow.argsFrom == -1 || n < allow.argsFrom {
						allow.argsFrom = n
					}
				} else {
					if allow.args == nil {
						allow.args = map[int]bool{}
					}
					allow.args[n] = true
				}
			}
		case strings.HasPrefix(token, "flag:"):
			if allow.flags == nil {
				allow.flags = map[string]bool{}
			}
			allow.flags[strings.TrimPrefix(token, "flag:")] = true
		}
	}
	return allow
}

// Arg reports whether "-" is allowed at positional index i.
func (a Allow) Arg(i int) bool {
	return a.args[i] || (a.argsFrom != -1 && i >= a.argsFrom)
}

// Flag reports whether "-" is allowed as the named flag's value.
func (a Allow) Flag(name string) bool {
	return a.flags[name]
}

// Empty reports whether the Allow permits "-" nowhere.
func (a Allow) Empty() bool {
	return len(a.args) == 0 && a.argsFrom == -1 && len(a.flags) == 0
}

// IsPiped reports whether the reader carries piped (redirected) input rather
// than an interactive terminal. A non-*os.File reader — the cmd.SetIn test
// seam — always counts as piped. For a real file, a character device means a
// terminal; anything else (pipe, regular file redirect) is piped input.
func IsPiped(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// IsTerminal reports whether the reader is an interactive terminal. A
// non-*os.File reader — the cmd.SetIn test seam — never is. Unlike IsPiped
// this asks term.IsTerminal, so /dev/null (a character device that delivers
// nothing) is not a terminal: a secret redirected from it should read as
// empty, not be refused as typed.
func IsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && isTerminal(f)
}

// InteractiveStdio reports whether both stdout and stdin are terminals — the
// floor for launching anything that draws to the terminal and reads
// keystrokes. A TUI (picker, wizard) reads key events from stdin, so a pipe or
// redirected file can never drive one — and when the command is consuming
// piped content (a "-" stdin input), a TUI would eat that content as key
// events.
//
// This asks term.IsTerminal, not whether the file is a character device. The
// two differ on exactly the case that matters: /dev/null is a character device
// that delivers no keystrokes, and `cmd < /dev/null` from a terminal session is
// how an agent says "I have nothing to type". Bubble Tea agrees — it tests
// the same term.IsTerminal, and when stdin fails that test it does not error, it opens
// /dev/tty and waits on the real terminal instead. Calling /dev/null
// interactive is therefore a hang, not a cosmetic mismatch.
//
// IsPiped above deliberately keeps the character-device test: it answers a
// different question (is there content on stdin to read?), and reading
// /dev/null correctly yields nothing.
func InteractiveStdio() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// InteractivePrompt reports whether stdin and stderr are terminals — the floor
// for a huh form specifically, because huh draws the form to stderr rather than
// stdout (huh form.go:112 passes tea.WithOutput(os.Stderr)), while a bare
// bubbletea program such as the picker draws to stdout.
//
// The distinction is not pedantry. Checking stdout for a form that renders to
// stderr means `cmd 2>somewhere` draws the prompt into the void while still
// reading /dev/tty: an invisible question blocking a terminal. Ask about the
// stream the launcher actually writes to.
func InteractivePrompt() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stderr)
}

// isTerminal uses charmbracelet/x/term, the same package Bubble Tea asks —
// both v1 (tea.go:25) and v2 (tea.go:34) import it — so this floor and the
// /dev/tty fallback it exists to prevent cannot disagree about what a terminal
// is.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(f.Fd())
}
