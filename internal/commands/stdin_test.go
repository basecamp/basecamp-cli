package commands

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// devNullStdin wires a command's stdin to /dev/null, a character device — the
// established TTY stand-in (see edit_test.go).
func devNullStdin(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	t.Cleanup(func() { devNull.Close() })
	cmd.SetIn(devNull)
}

func requireUsageErr(t *testing.T, err error) *output.Error {
	t.Helper()
	require.Error(t, err)
	var outErr *output.Error
	require.True(t, errors.As(err, &outErr), "expected *output.Error, got %T: %v", err, err)
	assert.Equal(t, output.CodeUsage, outErr.Code)
	return outErr
}

func TestReadStdinContentTrimsTrailingNewlines(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader("🎉\n"))

	content, err := readStdinContent(cmd, "<content>")
	require.NoError(t, err)
	assert.Equal(t, "🎉", content)
}

// CRLF pipes (Windows tools, curl -w) must not leave a stray \r behind — it
// would count against boost's 16-rune limit and corrupt titles.
func TestReadStdinContentTrimsTrailingCRLF(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader("exactly16chars!!\r\n"))

	content, err := readStdinContent(cmd, "<content>")
	require.NoError(t, err)
	assert.Equal(t, "exactly16chars!!", content)
}

func TestReadStdinContentTTYIsUsageErrorWithEscapeHints(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	devNullStdin(t, cmd)

	_, err := readStdinContent(cmd, "<content>")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "nothing is piped")
	assert.Contains(t, outErr.Hint, "heredoc")
	assert.NotContains(t, outErr.Hint, "--edit", "no --edit flag on this command")
}

// The hint must name the input that actually carried the "-". Suggesting a
// bare trailing "-" for a flag-borne dash would exceed the command's
// positional arity — an escape the caller cannot use.
func TestReadStdinContentTTYHintNamesTheFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "post <path>"}
	devNullStdin(t, cmd)

	_, err := readStdinContent(cmd, "--data")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Hint, "--data -")
	assert.NotContains(t, outErr.Hint, "... -)", "a bare positional dash would break arity")
}

func TestReadStdinContentTTYHintMentionsEditWhenAvailable(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.Flags().Bool("edit", false, "")
	devNullStdin(t, cmd)

	_, err := readStdinContent(cmd, "<content>")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Hint, "--edit")
}

func TestReadStdinContentBlankPipeIsUsageError(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader("  \n\n"))

	_, err := readStdinContent(cmd, "<content>")
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "empty")
}

// The cap is a read bound, so it is checked against the bytes read rather than
// the trimmed content: exactly maxStdinContent is content, one byte past it is
// a refusal — including when that byte is the trailing newline every pipe ends
// with, which is the only way to stop reading at the cap at all.
func TestReadStdinContentAtTheCapIsAccepted(t *testing.T) {
	body := strings.Repeat("a", maxStdinContent)
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader(body))

	content, err := readStdinContent(cmd, "<content>")
	require.NoError(t, err)
	assert.Equal(t, body, content)
}

func TestReadStdinContentOverTheCapIsUsageError(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"one byte over", strings.Repeat("a", maxStdinContent+1)},
		{"the cap plus a trailing newline", strings.Repeat("a", maxStdinContent) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "x"}
			cmd.SetIn(strings.NewReader(tc.body))

			_, err := readStdinContent(cmd, "<content>")
			outErr := requireUsageErr(t, err)
			assert.Contains(t, outErr.Message, "exceeds 1048576 bytes")
		})
	}
}

func TestResolveContentArgJoinsLiteralArgs(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	content, err := resolveContentArg(cmd, []string{"hello", "world"}, 1)
	require.NoError(t, err)
	assert.Equal(t, "hello world", content)
}

func TestResolveContentArgLoneDashReadsStdin(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader("from stdin\n"))

	content, err := resolveContentArg(cmd, []string{"-"}, 1)
	require.NoError(t, err)
	assert.Equal(t, "from stdin", content)
}

func TestResolveContentArgDashAmongOthersIsUsageError(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader("from stdin"))

	_, err := resolveContentArg(cmd, []string{"-", "extra"}, 1)
	outErr := requireUsageErr(t, err)
	assert.Contains(t, outErr.Message, "only")
}

// A "-" placed after the -- separator is literal: parsed through a real
// Execute so ArgsLenAtDash is set.
func TestResolveContentArgDashAfterSeparatorIsLiteral(t *testing.T) {
	var content string
	cmd := &cobra.Command{
		Use: "x <id> <content>",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			content, err = resolveContentArg(cmd, args[1:], 1)
			return err
		},
	}
	cmd.SetIn(strings.NewReader("must not be read"))
	cmd.SetArgs([]string{"123", "--", "-"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, "-", content)
}

func TestResolveContentValueFlagDashReadsStdin(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	cmd.SetIn(strings.NewReader("flag body\n"))

	content, err := resolveContentValue(cmd, "-", -1, "--data")
	require.NoError(t, err)
	assert.Equal(t, "flag body", content)
}

func TestResolveContentValueLiteralPassesThrough(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	content, err := resolveContentValue(cmd, "plain", -1, "--data")
	require.NoError(t, err)
	assert.Equal(t, "plain", content)
}

func TestResolveContentValuePositionalDashAfterSeparatorIsLiteral(t *testing.T) {
	var body string
	cmd := &cobra.Command{
		Use: "x <title> [body]",
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			body, err = resolveContentValue(cmd, args[1], 1, "[body]")
			return err
		},
	}
	cmd.SetIn(strings.NewReader("must not be read"))
	cmd.SetArgs([]string{"--", "title", "-"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})

	require.NoError(t, cmd.Execute())
	assert.Equal(t, "-", body)
}

func TestAllowDashMergesTokens(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	allowDash(cmd, "arg:0")
	allowDash(cmd, "flag:description")
	assert.Equal(t, "arg:0 flag:description", cmd.Annotations["allow_dash"])
}
