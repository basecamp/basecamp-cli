package stdinarg

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAllowExactArg(t *testing.T) {
	allow := ParseAllow("arg:0")
	assert.True(t, allow.Arg(0))
	assert.False(t, allow.Arg(1))
	assert.False(t, allow.Flag("data"))
	assert.False(t, allow.Empty())
}

func TestParseAllowOpenEndedArg(t *testing.T) {
	allow := ParseAllow("arg:1+")
	assert.False(t, allow.Arg(0))
	assert.True(t, allow.Arg(1))
	assert.True(t, allow.Arg(5))
}

func TestParseAllowFlags(t *testing.T) {
	allow := ParseAllow("flag:data flag:out")
	assert.True(t, allow.Flag("data"))
	assert.True(t, allow.Flag("out"))
	assert.False(t, allow.Flag("body"))
	assert.False(t, allow.Arg(0))
}

func TestParseAllowMixed(t *testing.T) {
	allow := ParseAllow("arg:0 arg:2+ flag:description")
	assert.True(t, allow.Arg(0))
	assert.False(t, allow.Arg(1))
	assert.True(t, allow.Arg(2))
	assert.True(t, allow.Arg(3))
	assert.True(t, allow.Flag("description"))
}

func TestParseAllowEmptyAndGarbage(t *testing.T) {
	assert.True(t, ParseAllow("").Empty())
	assert.True(t, ParseAllow("arg:x bogus flag").Empty())
}

func TestIsPipedNonFileReader(t *testing.T) {
	assert.True(t, IsPiped(strings.NewReader("piped")))
}

func TestIsPipedCharDevice(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devNull.Close()

	assert.False(t, IsPiped(devNull))
}

func TestIsPipedRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	require.NoError(t, err)
	defer f.Close()

	assert.True(t, IsPiped(f))
}

// openPTY returns the master side of a new pseudo-terminal — the only thing
// term.IsTerminal accepts. /dev/null will not stand in: it is a character
// device but not a terminal, and that gap is what these predicates exist to
// close.
func openPTY(t *testing.T) *os.File {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("no /dev/ptmx on Windows")
	}
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// stdioMatrix exercises a predicate over each of its two endpoints. The
// terminal/terminal row is the load-bearing one: without a passing positive
// baseline every other row would hold for a predicate hardwired to false, and
// the test would bless a floor that refuses everything.
//
// pipe and /dev/null are the two ways a stream arrives without a human. Both
// have to fail, and /dev/null is the one that used to pass: a character device
// that is not a terminal. Bubble Tea does not error on one, it opens /dev/tty
// and waits on the real terminal — so classifying it interactive is a hang.
func stdioMatrix(t *testing.T, name string, predicate func() bool, first, second **os.File) {
	t.Helper()

	pty := openPTY(t)

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	require.NoError(t, err)
	defer devnull.Close()

	pipeR, pipeW, err := os.Pipe()
	require.NoError(t, err)
	defer pipeR.Close()
	defer pipeW.Close()

	origFirst, origSecond := *first, *second
	t.Cleanup(func() { *first, *second = origFirst, origSecond })

	for _, tc := range []struct {
		label       string
		first       *os.File
		second      *os.File
		interactive bool
	}{
		{"terminal/terminal", pty, pty, true},
		{"pipe/terminal", pipeR, pty, false},
		{"terminal/pipe", pty, pipeW, false},
		{"devnull/terminal", devnull, pty, false},
		{"terminal/devnull", pty, devnull, false},
	} {
		*first, *second = tc.first, tc.second
		assert.Equal(t, tc.interactive, predicate(), "%s with %s", name, tc.label)
	}
}

// TestInteractiveStdio covers the picker's pair: a bare bubbletea program reads
// keystrokes from stdin and draws to stdout.
func TestInteractiveStdio(t *testing.T) {
	stdioMatrix(t, "InteractiveStdio", InteractiveStdio, &os.Stdin, &os.Stdout)
}

// TestInteractivePrompt covers huh's pair. huh draws the form to stderr
// (form.go:112 passes tea.WithOutput(os.Stderr)), so asking about stdout would
// let `cmd 2>somewhere` render an invisible question that still blocks a
// terminal — and would refuse `cmd | less` where the prompt would have worked.
func TestInteractivePrompt(t *testing.T) {
	stdioMatrix(t, "InteractivePrompt", InteractivePrompt, &os.Stdin, &os.Stderr)
}

// TestPredicatesDisagreeOnTheStreamTheyAskAbout pins the reason there are two:
// a terminal stdin with a piped stdout and a terminal stderr is exactly the
// shape where a form works and a picker does not.
func TestPredicatesDisagreeOnTheStreamTheyAskAbout(t *testing.T) {
	pty := openPTY(t)

	pipeR, pipeW, err := os.Pipe()
	require.NoError(t, err)
	defer pipeR.Close()
	defer pipeW.Close()

	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	t.Cleanup(func() { os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr })

	os.Stdin, os.Stdout, os.Stderr = pty, pipeW, pty

	assert.False(t, InteractiveStdio(), "a piped stdout is no place for a picker")
	assert.True(t, InteractivePrompt(), "but a form draws to stderr, which is still a terminal")
}

// TestIsTerminal: only a real terminal counts. A buffer (the cmd.SetIn
// seam), a pipe, and /dev/null — a character device that delivers nothing —
// are all "not a terminal", so a secret redirected from any of them is read
// rather than refused as typed.
func TestIsTerminal(t *testing.T) {
	assert.False(t, IsTerminal(strings.NewReader("token")))

	devnull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer devnull.Close()
	assert.False(t, IsTerminal(devnull))

	pipeR, pipeW, err := os.Pipe()
	require.NoError(t, err)
	defer pipeR.Close()
	defer pipeW.Close()
	assert.False(t, IsTerminal(pipeR))

	pty := openPTY(t)
	assert.True(t, IsTerminal(pty))
}
