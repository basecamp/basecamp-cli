package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// TurnResult is a finished turn.
type TurnResult struct {
	// SessionID is the session the CLI confirmed, which is the one to resume.
	// Empty when the turn failed before the CLI opened one.
	SessionID string
	Model     string
	// Text is the agent's answer: what it said last.
	Text string
	// Usage is what the turn cost, where the CLI says so per turn.
	Usage Usage
	// SessionCostUSD is what the session has cost in all, for a CLI that
	// reports that instead; zero for one that does not.
	SessionCostUSD float64
	// Refused names the tools whose calls the CLI refused, one entry per
	// call, where the CLI says.
	Refused []string
}

// ErrTurnFailed wraps a turn the CLI ran and reported failed, as opposed to
// one that could not be started.
var ErrTurnFailed = errors.New("the turn failed")

// turnGrace is how long a turn's process group has between SIGTERM and
// SIGKILL when the runner ends it.
const turnGrace = 5 * time.Second

// maxLineBytes bounds one line of an agent's stream. Pi's last line carries
// the turn's whole transcript, so the bound is generous; past it the line is
// skipped rather than held.
const maxLineBytes = 64 << 20

// turnRequest is everything one turn needs.
type turnRequest struct {
	Adapter Adapter
	Turn    Turn
	Prompt  string
	Env     []string
	// Transcript takes the CLI's raw output, for a person diagnosing a turn.
	// It is written as the CLI wrote it, unredacted, which is deliberate: it
	// holds no more than the session files the CLI keeps of its own accord,
	// in a directory as private as the connector's state. Nothing read from
	// it is logged or sent anywhere.
	Transcript io.Writer
	// Events is called for each event as it is read, on the reading
	// goroutine.
	Events func(Event)
	// LookPath finds the CLI. Nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Started is called with the CLI's process as soon as it exists.
	Started func(driver.Process)
}

// runTurn runs one turn to its end: it starts the CLI as the leader of a
// process group of its own, writes the prompt to its standard input, reads
// its stream, and ends the group whichever way the turn went. A ctx that
// ends stops the turn.
func runTurn(ctx context.Context, req turnRequest) (TurnResult, error) {
	look := req.LookPath
	if look == nil {
		look = exec.LookPath
	}
	path, err := look(req.Adapter.Binary())
	if err != nil {
		return TurnResult{}, fmt.Errorf("%s was not found on PATH: %w", req.Adapter.Binary(), err)
	}
	worker, err := driver.StartWorker(ctx, driver.SessionConfig{Scope: driver.Scope{WorkDir: req.Turn.Dir}, Started: req.Started},
		driver.Command{Path: path, Args: req.Adapter.Args(req.Turn), Env: req.Env})
	if err != nil {
		return TurnResult{}, err
	}
	if ctx.Err() != nil {
		// Ended as it started, by what Started made of it or by a stop: the
		// agent is told nothing.
		worker.Terminate(turnGrace)
		return TurnResult{}, ctx.Err()
	}
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			worker.Terminate(turnGrace)
		case <-finished:
		}
	}()
	go func() {
		// A CLI that exits without reading its input closes the pipe; that
		// shows in how the turn ended, not here.
		_, _ = io.WriteString(worker.Stdin(), req.Prompt)
		_ = worker.Stdin().Close()
	}()
	go func() {
		// Something the agent started may hold the output open after the CLI
		// itself is gone. The turn ended when the CLI did.
		select {
		case <-worker.Done():
			time.AfterFunc(2*time.Second, worker.CloseStdout)
		case <-finished:
		}
	}()

	var (
		result   TurnResult
		lastText string
		done     bool
		failure  string
	)
	reader := bufio.NewReaderSize(worker.Stdout(), 64<<10)
	for {
		line, tooLong, readErr := readLine(reader, maxLineBytes)
		if len(line) > 0 && !tooLong {
			if req.Transcript != nil {
				_, _ = req.Transcript.Write(line)
			}
			for _, event := range req.Adapter.Parse(line) {
				if event.SessionCost {
					result.SessionCostUSD = event.Usage.CostUSD
					event.Usage.CostUSD = 0
				}
				result.Usage = result.Usage.plus(event.Usage)
				result.Refused = append(result.Refused, event.Refused...)
				if event.Model != "" {
					result.Model = event.Model
				}
				if event.SessionID != "" {
					result.SessionID = event.SessionID
				}
				switch event.Kind {
				case EventText:
					lastText = event.Text
				case EventResult:
					done = !event.Failed
					if event.Failed {
						failure = cmpText(event.Text, "the agent reported an error")
					} else {
						failure = ""
						if event.Text != "" {
							lastText = event.Text
						}
					}
				case EventError:
					done, failure = false, cmpText(event.Text, "the agent reported an error")
				case EventSession, EventTool, EventUsage:
				}
				if req.Events != nil {
					req.Events(event)
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	exit := worker.Exit()
	// Whatever the agent left running in its group ends with the turn.
	worker.Terminate(turnGrace)
	result.Text = strings.TrimSpace(lastText)

	switch {
	case ctx.Err() != nil:
		return result, ctx.Err()
	case failure != "":
		return result, fmt.Errorf("%w: %s", ErrTurnFailed, failure)
	case done:
		return result, nil
	case exit.Code != 0 || exit.Signaled || exit.Err != nil:
		// This reaches the log, the worker's record and the orchestrator, so
		// what the agent's environment held is taken out of it.
		detail := worker.StderrTail(driver.NewRedactor(driver.Redaction{Env: req.Env}))
		return result, fmt.Errorf("%w: %s exited with status %d: %s", ErrTurnFailed, req.Adapter.Binary(), exit.Code, cmpText(detail, "no error output"))
	case result.Text != "":
		// The CLI ended cleanly and said something, without a line that
		// marks the end: its answer is what it said.
		return result, nil
	}
	return result, fmt.Errorf("%w: %s ended without an answer", ErrTurnFailed, req.Adapter.Binary())
}

// readLine reads one line, however long. A line past limit is consumed and
// reported as too long, with nothing kept of it.
func readLine(r *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	for {
		chunk, readErr := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > limit {
				line, tooLong = nil, true
			} else {
				line = append(line, chunk...)
			}
		}
		if readErr == nil {
			return line, tooLong, nil
		}
		if !errors.Is(readErr, bufio.ErrBufferFull) {
			return line, tooLong, readErr
		}
	}
}

// cmpText is text, or fallback when text is blank.
func cmpText(text, fallback string) string {
	if strings.TrimSpace(text) == "" {
		return fallback
	}
	return strings.TrimSpace(text)
}
