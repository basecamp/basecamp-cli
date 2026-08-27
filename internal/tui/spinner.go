package tui

import (
	"fmt"
	"io"
	"time"

	"charm.land/lipgloss/v2"
)

const (
	spinnerDelay    = 150 * time.Millisecond
	spinnerInterval = 80 * time.Millisecond
)

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// RunWithSpinner displays message while task runs on a terminal, clears the
// transient line when task finishes, and returns the task error. Fast tasks and
// non-terminal writers complete without transient output.
func RunWithSpinner(w io.Writer, theme Theme, message string, task func() error) error {
	if !isWriterTTY(w) {
		return task()
	}

	done := make(chan error, 1)
	go func() {
		done <- task()
	}()

	delay := time.NewTimer(spinnerDelay)
	defer delay.Stop()
	select {
	case err := <-done:
		return err
	case <-delay.C:
	}

	spinnerStyle := lipgloss.NewStyle().Foreground(theme.Primary)
	messageStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	frame := 0
	fmt.Fprint(w, "\033[?25l")
	defer fmt.Fprint(w, "\r\033[2K\033[?25h")

	for {
		fmt.Fprintf(w, "\r%s %s", spinnerStyle.Render(spinnerFrames[frame]), messageStyle.Render(message))
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			frame = (frame + 1) % len(spinnerFrames)
		}
	}
}
