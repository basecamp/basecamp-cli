package resolve

import (
	"os"
	"testing"
)

// TestIsInteractiveRequiresStdinCharDevice proves pickers are gated off when
// stdin is piped: a Bubble Tea picker reads keystrokes from stdin, so piped
// stdin can never drive one — and when a command is consuming piped content
// (a "-" stdin input), a picker would eat that content as key events.
func TestIsInteractiveRequiresStdinCharDevice(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "")

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

	// /dev/null is a character device, so it stands in for a terminal on
	// both ends without needing a PTY.
	os.Stdout = devnull

	r := New(nil, nil, nil)

	os.Stdin = devnull
	if !r.IsInteractive() {
		t.Fatal("expected interactive with char-device stdout and stdin")
	}

	os.Stdin = pipeR
	if r.IsInteractive() {
		t.Fatal("expected non-interactive with piped stdin: a picker would consume the pipe as key events")
	}
}
