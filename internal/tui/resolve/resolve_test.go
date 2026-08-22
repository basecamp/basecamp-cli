package resolve

import (
	"os"
	"runtime"
	"testing"
)

// openPTY returns the master side of a new pseudo-terminal, which is what
// term.IsTerminal actually accepts. /dev/null will not do: it is a character
// device but not a terminal, and treating it as one is the bug this file
// guards.
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

// TestIsInteractiveRequiresTerminalStdio proves pickers are gated off unless
// both ends are real terminals. A Bubble Tea picker reads keystrokes from
// stdin, so piped stdin can never drive one — and when a command is consuming
// piped content (a "-" stdin input), a picker would eat that content as key
// events.
//
// The /dev/null case is the one worth stating out loud: it is a character
// device, so the older character-device test called it interactive. Bubble Tea
// disagrees — it sees a non-terminal stdin, opens /dev/tty and waits on the
// real terminal — so `basecamp ... < /dev/null` from a terminal session hung.
func TestIsInteractiveRequiresTerminalStdio(t *testing.T) {
	t.Setenv("BASECAMP_NONINTERACTIVE", "")

	pty := openPTY(t)

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

	os.Stdout = pty

	r := New(nil, nil, nil)

	os.Stdin = pty
	if !r.IsInteractive() {
		t.Fatal("expected interactive with terminal stdout and stdin")
	}

	os.Stdin = pipeR
	if r.IsInteractive() {
		t.Fatal("expected non-interactive with piped stdin: a picker would consume the pipe as key events")
	}

	os.Stdin = devnull
	if r.IsInteractive() {
		t.Fatal("expected non-interactive with stdin on /dev/null: a picker there waits on /dev/tty forever")
	}

	os.Stdin = pty
	os.Stdout = devnull
	if r.IsInteractive() {
		t.Fatal("expected non-interactive with stdout on /dev/null")
	}
}
