package richtext

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellQuote(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"inert":          {"agent", "agent"},
		"inert symbols":  {"a-b_c.d/e:f@g%h+i=j", "a-b_c.d/e:f@g%h+i=j"},
		"empty":          {"", "''"},
		"space":          {"two words", "'two words'"},
		"semicolon":      {"a;rm -rf /", "'a;rm -rf /'"},
		"substitution":   {"$(id)", "'$(id)'"},
		"backtick":       {"`id`", "'`id`'"},
		"single quote":   {"it's", `'it'\''s'`},
		"only a quote":   {"'", `''\'''`},
		"quote and semi": {"'; id; '", `''\''; id; '\'''`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, ShellQuote(tc.in))
		})
	}
}

// The encoding is only worth anything if a real shell reads it back as the
// one word that went in — an embedded single quote being the case that
// tells quoting apart from wrapping.
func TestShellQuoteSurvivesARealShell(t *testing.T) {
	for _, in := range []string{
		"agent", "", "two words", "a;rm -rf /", "$(echo pwned)", "`echo pwned`",
		"it's", "'", "'; echo pwned; '", "a\nb", `back\slash`, "*", "~root",
	} {
		out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf %s "+ShellQuote(in)).Output()
		require.NoError(t, err, "input %q quoted as %s", in, ShellQuote(in))
		assert.Equal(t, in, string(out), "input %q quoted as %s", in, ShellQuote(in))
	}
}

// And the word count is one: a value with a space in it must not split into
// two arguments.
func TestShellQuoteKeepsAValueOneWord(t *testing.T) {
	for _, in := range []string{"two words", "'; echo pwned; '", "a b c"} {
		out, err := exec.CommandContext(t.Context(), "/bin/sh", "-c", "set -- "+ShellQuote(in)+"; echo $#").Output()
		require.NoError(t, err)
		assert.Equal(t, "1", strings.TrimSpace(string(out)), "input %q", in)
	}
}
