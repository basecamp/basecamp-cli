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

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

var approvalFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const approvalInterval = 100 * time.Millisecond

// startApprovalWait begins drawing on w when it is a terminal and returns
// nil otherwise, so callers can treat "no live line" uniformly: Stop on a
// nil *approvalWait is a no-op.
func startApprovalWait(w io.Writer, deadline time.Time) *approvalWait {
	if !writerIsTerminal(w) {
		return nil
	}
	return runApprovalWait(w, deadline, time.Now, approvalInterval)
}

// runApprovalWait is the injectable core: tests drive it with a buffer, a
// fixed clock, and a short interval.
func runApprovalWait(w io.Writer, deadline time.Time, now func() time.Time, interval time.Duration) *approvalWait {
	a := &approvalWait{
		w:        w,
		deadline: deadline,
		now:      now,
		interval: interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go a.run()
	return a
}

func (a *approvalWait) run() {
	defer close(a.done)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	frame := 0
	for {
		fmt.Fprintf(a.w, "\r\033[2K%s Waiting for approval… code expires in %s", approvalFrames[frame], remaining(a.deadline.Sub(a.now())))
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

func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}
