//go:build linux || darwin

package connect

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// Messages the connector logs that tests wait on.
const (
	// MsgRunning is logged once the connector holds its lock and its
	// ledger, just before intake starts. A signal before it may land
	// before the connector's handler is installed.
	MsgRunning = "connector: running"
	// MsgStreaming is logged each time the feed's catch-up walk reaches the
	// head with the live buffer drained: from then on an event published
	// live is delivered as it is published.
	MsgStreaming = "feed walk reached its head and the buffer drained"
)

// exitInterrupted is how the connector exits on SIGINT.
const exitInterrupted = 130

// Connector is one CLI process the test runs in the background, usually
// `basecamp connect`, with everything it writes read as it is written.
type Connector struct {
	t    *testing.T
	what string
	cmd  *exec.Cmd
	// exited is closed once the process has exited and both of its outputs
	// are read to the end.
	exited chan struct{}

	mu sync.Mutex
	// changed is closed, and replaced, whenever out changes.
	changed chan struct{}
	out     Output
}

// Output is everything a Connector has written so far, and how it ended
// once it has.
type Output struct {
	// Lines are its stdout lines, in order.
	Lines []Line
	// Logs are its stderr lines, in order.
	Logs []LogLine
	// Exited is set once the process has exited and its output is all read;
	// ExitCode is then its exit code, -1 when a signal killed it.
	Exited   bool
	ExitCode int
}

// Connect starts `basecamp connect -P agent` with extra flags, in the
// harness's home.
func (h *Harness) Connect(t *testing.T, extra ...string) *Connector {
	t.Helper()
	return h.Start(t, h.Home, append([]string{"connect", "-P", Agent}, extra...)...)
}

// Start starts a CLI command in the background, in dir, with the harness's
// environment. Whatever it is doing when the test ends, it is killed (the
// test's context ends just before its cleanup runs) and waited for, and its
// output is logged when the test failed.
func (h *Harness) Start(t *testing.T, dir string, args ...string) *Connector {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary(t), args...)
	cmd.Env = h.env
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderr, err := cmd.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	c := &Connector{
		t:       t,
		what:    "basecamp " + strings.Join(args, " "),
		cmd:     cmd,
		exited:  make(chan struct{}),
		changed: make(chan struct{}),
	}
	var readers sync.WaitGroup
	readers.Go(func() {
		readLines(stdout, func(raw string, complete bool) {
			line := parseLine(raw)
			if !complete && line.Err == nil {
				line.Err = errors.New("the line was not ended: torn")
			}
			c.update(func(o *Output) { o.Lines = append(o.Lines, line) })
		})
	})
	readers.Go(func() {
		readLines(stderr, func(raw string, _ bool) {
			c.update(func(o *Output) { o.Logs = append(o.Logs, parseLogLine(raw)) })
		})
	})
	go func() {
		// Wait closes the pipes, so it waits for the readers to reach EOF
		// first: the process's exit closes its ends.
		readers.Wait()
		err := cmd.Wait()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			code = -1
		}
		c.update(func(o *Output) { o.Exited, o.ExitCode = true, code })
		close(c.exited)
	}()
	t.Cleanup(c.cleanup)
	return c
}

// readLines calls fn with each line r yields, without its newline, until
// r ends. complete is false for a last line that had no newline.
func readLines(r io.Reader, fn func(raw string, complete bool)) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if strings.HasSuffix(line, "\n") {
			fn(strings.TrimSuffix(line, "\n"), true)
		} else if line != "" {
			fn(line, false)
		}
		if err != nil {
			return
		}
	}
}

func (c *Connector) update(fn func(o *Output)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.out)
	close(c.changed)
	c.changed = make(chan struct{})
}

// Output is what the process has written so far.
func (c *Connector) Output() Output {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.snapshot()
}

func (o *Output) snapshot() Output {
	return Output{
		Lines:    append([]Line(nil), o.Lines...),
		Logs:     append([]LogLine(nil), o.Logs...),
		Exited:   o.Exited,
		ExitCode: o.ExitCode,
	}
}

// WaitFor returns the output once cond holds of it. It fails the test when
// the process exits with cond still false, or when waitFor passes first.
// cond is called again whenever the process writes, never on a timer.
func (c *Connector) WaitFor(t *testing.T, what string, cond func(o Output) bool) Output {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitFor)
	defer cancel()
	for {
		c.mu.Lock()
		out, changed := c.out.snapshot(), c.changed
		c.mu.Unlock()
		if cond(out) {
			return out
		}
		if out.Exited {
			t.Fatalf("%s exited with code %d before %s\n%s", c.what, out.ExitCode, what, out.Transcript())
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("%s: no %s within %s\n%s", c.what, what, waitFor, c.Output().Transcript())
		}
	}
}

// WaitLogged waits for the nth (from 1) stderr record with msg.
func (c *Connector) WaitLogged(t *testing.T, msg string, nth int) Output {
	t.Helper()
	return c.WaitFor(t, "log line "+msg, func(o Output) bool { return len(o.Logged(msg)) >= nth })
}

// WaitRunning waits for the connector to say it is running.
func (c *Connector) WaitRunning(t *testing.T) {
	t.Helper()
	c.WaitLogged(t, MsgRunning, 1)
}

// WaitStreaming waits for the nth time the feed has caught up and is
// streaming: 1 for the first connection, one more for each reconnect.
func (c *Connector) WaitStreaming(t *testing.T, nth int) {
	t.Helper()
	c.WaitLogged(t, MsgStreaming, nth)
}

// Signal sends sig to the process as it stands. Interrupt is the safe way
// to send SIGINT.
func (c *Connector) Signal(t *testing.T, sig os.Signal) {
	t.Helper()
	require.NoError(t, c.cmd.Process.Signal(sig))
}

// Interrupt sends SIGINT once the connector has said it is running, never
// before: a signal that lands before its handler is installed kills it
// without the shutdown under test.
func (c *Connector) Interrupt(t *testing.T) {
	t.Helper()
	c.WaitRunning(t)
	c.Signal(t, syscall.SIGINT)
}

// Kill kills the process outright, as kill -9 does.
func (c *Connector) Kill(t *testing.T) {
	t.Helper()
	c.Signal(t, syscall.SIGKILL)
}

// Wait waits for the process to exit and its output to be read, and returns
// its output, whose ExitCode is how it ended.
func (c *Connector) Wait(t *testing.T) Output {
	t.Helper()
	return c.WaitFor(t, "exit", func(o Output) bool { return o.Exited })
}

// Stop interrupts the connector, as a person's Ctrl-C does, and requires
// that it shuts down as it should for one. Everything it wrote is then in
// the output it returns: assertions that something was not written belong
// after it.
func (c *Connector) Stop(t *testing.T) Output {
	t.Helper()
	c.Interrupt(t)
	out := c.Wait(t)
	require.Equal(t, exitInterrupted, out.ExitCode, "exit code on SIGINT\n%s", out.Transcript())
	return out
}

// cleanup waits for the process, which the end of the test's context has
// killed if it was still running, and holds every run to what any run must
// do: write only protocol lines to stdout, and provoke no race.
func (c *Connector) cleanup() {
	t := c.t
	<-c.exited
	out := c.Output()
	for i, l := range out.Lines {
		if l.Err != nil {
			t.Errorf("%s: stdout line %d is not a protocol line (%v): %q", c.what, i+1, l.Err, l.Raw)
		}
	}
	raw := make([]string, len(out.Logs))
	for i, l := range out.Logs {
		raw[i] = l.Raw
	}
	requireNoRace(t, c.what, strings.Join(raw, "\n"))
	if t.Failed() {
		t.Logf("%s\n%s", c.what, out.Transcript())
	}
}

// Transcript is the output as written, for a failure message.
func (o Output) Transcript() string {
	var b strings.Builder
	b.WriteString("--- stdout\n")
	for _, l := range o.Lines {
		b.WriteString(l.Raw + "\n")
	}
	b.WriteString("--- stderr\n")
	for _, l := range o.Logs {
		b.WriteString(l.Raw + "\n")
	}
	return b.String()
}

// Pointers are the pointer lines written so far.
func (o Output) Pointers() []PointerLine {
	var out []PointerLine
	for _, l := range o.Lines {
		if l.Pointer != nil {
			out = append(out, *l.Pointer)
		}
	}
	return out
}

// Events are the verdict lines written so far.
func (o Output) Events() []EventLine {
	var out []EventLine
	for _, l := range o.Lines {
		if l.Event != nil {
			out = append(out, *l.Event)
		}
	}
	return out
}

// Requests are the request lines written so far.
func (o Output) Requests() []RequestLine {
	var out []RequestLine
	for _, l := range o.Lines {
		if l.Request != nil {
			out = append(out, *l.Request)
		}
	}
	return out
}

// PointersFor are the pointer lines for one event.
func (o Output) PointersFor(eventID int64) []PointerLine {
	var out []PointerLine
	for _, p := range o.Pointers() {
		if p.EventID == eventID {
			out = append(out, p)
		}
	}
	return out
}

// EventsFor are the verdict lines for one event.
func (o Output) EventsFor(eventID int64) []EventLine {
	var out []EventLine
	for _, e := range o.Events() {
		if e.EventID == eventID {
			out = append(out, e)
		}
	}
	return out
}

// RequestsFor are the request lines for one event.
func (o Output) RequestsFor(eventID int64) []RequestLine {
	var out []RequestLine
	for _, r := range o.Requests() {
		if r.EventID == eventID {
			out = append(out, r)
		}
	}
	return out
}

// Logged are the stderr records with msg.
func (o Output) Logged(msg string) []LogLine {
	var out []LogLine
	for _, l := range o.Logs {
		if l.Msg == msg {
			out = append(out, l)
		}
	}
	return out
}
