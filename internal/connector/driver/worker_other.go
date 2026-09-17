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

func (*Worker) Process() Process        { return Process{} }
func (*Worker) Stdin() io.WriteCloser   { return nil }
func (*Worker) Stdout() io.Reader       { return nil }
func (*Worker) Done() <-chan struct{}   { return nil }
func (*Worker) Exit() Exit              { return Exit{} }
func (*Worker) StderrTail() string      { return "" }
func (*Worker) Terminate(time.Duration) {}

// TerminateRecorded does nothing off Unix.
func TerminateRecorded(Process, time.Duration) (bool, error) { return false, errUnsupported }
