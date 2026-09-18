//go:build !unix

package driver

import (
	"context"
	"errors"
	"io"
	"time"
)

var errUnsupported = errors.New("driver: workers run on Unix only (process groups)")

// Worker is unavailable off Unix.
type Worker struct{}

// StartWorker refuses off Unix; nothing is started.
func StartWorker(context.Context, Launcher, Scope, Command) (*Worker, error) {
	return nil, errors.Join(ErrNotStarted, errUnsupported)
}

func (*Worker) Process() Process               { return Process{} }
func (*Worker) Stdin() io.WriteCloser          { return nil }
func (*Worker) Stdout() io.Reader              { return nil }
func (*Worker) CloseStdout()                   {}
func (*Worker) Done() <-chan struct{}          { return nil }
func (*Worker) Exit() Exit                     { return Exit{} }
func (*Worker) StderrTail(*Redactor) string    { return "" }
func (*Worker) StderrLines(*Redactor) []string { return nil }
func (*Worker) Terminate(time.Duration)        {}

// OwnsWorker cannot answer off Unix, and an identity that cannot be
// established is never acted on.
func OwnsWorker(Process) (bool, error) { return false, errUnsupported }

// GroupMembersRemain cannot answer off Unix, and what cannot be proven gone
// is held: it answers that members remain.
func GroupMembersRemain(Process) bool { return true }

// ConfirmGroupGone cannot answer off Unix.
func ConfirmGroupGone(Process, time.Duration) error { return errUnsupported }

// OwnProcessGroup cannot answer off Unix.
func OwnProcessGroup() (int, bool) { return 0, false }

// ProcessGone cannot answer off Unix, and what cannot be answered is not
// proven gone.
func ProcessGone(Process) (bool, error) { return false, errUnsupported }

// LookupProcess cannot answer off Unix.
func LookupProcess(int) (Process, error) { return Process{}, errUnsupported }

// TerminateRecorded does nothing off Unix.
func TerminateRecorded(Process, time.Duration) (bool, error) { return false, errUnsupported }
