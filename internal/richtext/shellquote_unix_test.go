//go:build unix

package richtext

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These hand the encoding to a real shell, which is the only thing that
// settles whether it is right. /bin/sh is not there on Windows, which this
// repository builds for, so they live behind the unix tag and the pure
// encoding tests next door stay portable (Copilot on #765).

// The encoding is only worth anything if a real shell reads it back as the
// one word that went in — an embedded single quote being the case that
// tells quoting apart from wrapping.
func TestShellQuoteSurvivesARealShell(t *testing.T) {
	for _, in := range []string{
		"agent", "", "two words", "a;rm -rf /", "$(echo pwned)", "`echo pwned`",
		"it's", "'", "'; echo pwned; '", "a\nb", "a\tb", `back\slash`, "*", "~root",
		// The shapes the three deleted copies carried: a profile name
		// (auth), a config path (config), and an opaque feed position
		// (commands). An apostrophe in a home directory is the case that
		// tells quoting apart from wrapping, and it is not hypothetical.
		"work profile", "/home/o'brien/.basecamp/config.json", "pos 1; rm -rf /",
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
