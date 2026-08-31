//go:build unix

package tui

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// DetectImageSupport asks the terminal whether it draws pictures and remembers the
// answer, which is what NewImageRenderer then hands out.
//
// It asks rather than guesses. A terminal's own environment variables outlive it —
// TERM_PROGRAM and GHOSTTY_RESOURCES_DIR are still set inside tmux, inside herdr,
// inside anything that runs a program in a pane — so they say nothing reliable about
// what is on the far end of this pty. A multiplexer that passes graphics through
// passes the query through too and the real terminal answers; one that does not
// swallows both, and the answer is no by the same rule. Nothing has to know the
// name of the thing in the middle.
//
// It runs before the Bubble Tea program owns the terminal, in raw mode so the
// answer is readable, and gives up quietly when either end is not a terminal or
// nothing comes back. The read polls with a deadline rather than blocking, so a
// terminal that never answers cannot stall the TUI or leave a reader stealing its
// input — the same shape CalibrateWidths uses, for the same reasons.
func DetectImageSupport(in, out *os.File) {
	if insideARelayThatEatsPictures(os.Getenv) {
		return
	}
	inFd, outFd := int(in.Fd()), int(out.Fd()) //nolint:gosec // G115: fds fit in int
	if !term.IsTerminal(inFd) || !term.IsTerminal(outFd) {
		return
	}
	restore, err := term.MakeRaw(inFd)
	if err != nil {
		return
	}
	defer term.Restore(inFd, restore) //nolint:errcheck

	if _, err := out.WriteString(imageProbeRequest()); err != nil {
		return
	}
	if draws, answered := readImageReply(in, imageProbeBudget); answered {
		drawsImages.Store(draws)
	}
}

// imageProbeBudget is how long the terminal has to answer. The device attributes
// request behind the query means an answer normally arrives in a round trip; this is
// the ceiling for a terminal that is slow or silent, and it is paid once at startup.
const imageProbeBudget = 300 * time.Millisecond

func readImageReply(in *os.File, budget time.Duration) (draws, answered bool) {
	deadline := time.Now().Add(budget)
	buf := make([]byte, 0, 256)
	chunk := make([]byte, 64)
	for {
		if draws, answered := readImageAnswer(buf); answered {
			return draws, true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, false
		}
		fds := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}} // #nosec G115 -- a poll fd fits in int32
		ready, err := unix.Poll(fds, int(remaining.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || ready == 0 {
			return false, false
		}
		n, err := in.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			draws, answered := readImageAnswer(buf)
			return draws, answered
		}
	}
}
