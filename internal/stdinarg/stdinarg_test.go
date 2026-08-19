package stdinarg

import (
	"os"
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

// TestInteractiveStdio proves TUIs are gated off when stdin is piped: a
// wizard or picker reads keystrokes from stdin, so piped stdin would be
// consumed as key events — including piped content meant for a "-" input.
func TestInteractiveStdio(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pipeR.Close()
	defer pipeW.Close()

	origOut, origIn := os.Stdout, os.Stdin
	t.Cleanup(func() { os.Stdout, os.Stdin = origOut, origIn })

	// /dev/null is a character device, standing in for a terminal on both
	// ends without needing a PTY.
	os.Stdout, os.Stdin = devnull, devnull
	if !InteractiveStdio() {
		t.Fatal("expected interactive with char-device stdout and stdin")
	}

	os.Stdin = pipeR
	if InteractiveStdio() {
		t.Fatal("expected non-interactive with piped stdin")
	}

	os.Stdout, os.Stdin = pipeW, devnull
	if InteractiveStdio() {
		t.Fatal("expected non-interactive with piped stdout")
	}
}
