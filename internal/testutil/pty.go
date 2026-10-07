// Package testutil provides shared test fixtures.
package testutil

import (
	"os"
	"runtime"
	"testing"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

// OpenPTY returns a terminal suitable for replacing standard streams in tests.
// Use the slave: macOS does not recognize the master as a terminal. Keep both
// ends open until cleanup, after callers have restored their standard streams.
func OpenPTY(t *testing.T) *os.File {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no Unix PTY on Windows")
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})
	if !term.IsTerminal(slave.Fd()) {
		t.Fatal("PTY slave is not a terminal")
	}
	return slave
}
