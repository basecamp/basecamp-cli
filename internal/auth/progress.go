package auth

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
)

// approvalWait is the live line a terminal shows while the device flow polls
// for approval: a spinner, the prompt, and the time left before the code
// expires, redrawn in place and cleared when the flow ends. It exists so a
// person staring at the terminal can tell the CLI is still working and how
// long the code is good for — a silent poll and a "waiting" line printed
// once are indistinguishable from a hang. Non-terminal writers get nothing
// drawn; the caller logs a static line for those.
type approvalWait struct {
	w        io.Writer
	deadline time.Time
	now      func() time.Time
	interval time.Duration
	// width is the terminal's column count, which picks the line's form:
	// the full sentence, a short one, or nothing at all when even that
	// would wrap — a wrapped line is redrawn from its continuation row and
	// leaves the previous row behind on every tick.
	width int

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

var approvalFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const approvalInterval = 100 * time.Millisecond

// Column budgets for the two forms of the live line, counted on the
// widest countdown they render ("99:59"); a terminal narrower than the
// short form draws nothing and the caller logs the static line instead.
const (
	approvalLineFullWidth  = len("⠋ Waiting for approval… code expires in 99:59") - 4 // multibyte glyphs count once
	approvalLineShortWidth = len("⠋ Waiting… 99:59") - 4
)

// startApprovalWait begins drawing on w when it is a terminal and returns
// nil otherwise, so callers can treat "no live line" uniformly: Stop on a
// nil *approvalWait is a no-op.
func startApprovalWait(w io.Writer, deadline time.Time) *approvalWait {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return nil
	}
	width, _, err := term.GetSize(f.Fd())
	if err != nil {
		width = 80
	}
	return runApprovalWait(w, deadline, time.Now, approvalInterval, width)
}

// runApprovalWait is the injectable core: tests drive it with a buffer, a
// fixed clock, a short interval, and a chosen width. A width too narrow
// for even the short form yields nil, like a non-terminal.
func runApprovalWait(w io.Writer, deadline time.Time, now func() time.Time, interval time.Duration, width int) *approvalWait {
	if width < approvalLineShortWidth {
		return nil
	}
	a := &approvalWait{
		w:        w,
		deadline: deadline,
		now:      now,
		interval: interval,
		width:    width,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go a.run()
	return a
}

// line renders one tick of the live line in the form the width allows.
func (a *approvalWait) line(frame int) string {
	left := remaining(a.deadline.Sub(a.now()))
	if a.width < approvalLineFullWidth {
		return fmt.Sprintf("%s Waiting… %s", approvalFrames[frame], left)
	}
	return fmt.Sprintf("%s Waiting for approval… code expires in %s", approvalFrames[frame], left)
}

func (a *approvalWait) run() {
	defer close(a.done)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	frame := 0
	for {
		fmt.Fprint(a.w, "\r\033[2K"+a.line(frame))
		select {
		case <-a.stop:
			fmt.Fprint(a.w, "\r\033[2K")
			return
		case <-ticker.C:
			frame = (frame + 1) % len(approvalFrames)
		}
	}
}

// Stop clears the line and waits for the drawing goroutine to finish, so
// nothing the caller prints next can interleave with a redraw.
func (a *approvalWait) Stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		close(a.stop)
		<-a.done
	})
}

// remaining renders a duration as m:ss, floored at 0:00 — the shape a person
// reads as a countdown, unlike time.Duration's "9m41.3s".
func remaining(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// expiresIn renders a code lifetime for a static line: whole minutes when it
// is one, seconds otherwise ("10 minutes", "90 seconds").
func expiresIn(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		if d == time.Minute {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	default:
		return fmt.Sprintf("%d seconds", int(d/time.Second))
	}
}
