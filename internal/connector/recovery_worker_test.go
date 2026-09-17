//go:build unix

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/connector/driver/drivertest"
)

// The fake worker: what every fake agent does with a prompt, whatever its wire.

// fakeWorker is what a worker does, whatever its wire: it reads its dispatch,
// acknowledges, replies and completes through the task token its MCP server
// declaration carries, as scripted per event.
type fakeWorker struct {
	dir      string
	sc       harnessScenario
	ledger   *Ledger
	dispatch *TaskDispatch
	// stopWatch ends the watch for the task token in files.
	stopWatch func() []string

	replies map[int64]int64
}

// agentLogEntry is one thing a fake agent did.
type agentLogEntry struct {
	PID       int       `json:"pid"`
	PGID      int       `json:"pgid"`
	StartedAt time.Time `json:"started_at"`
	Event     int64     `json:"event,omitempty"`
	N         int       `json:"n,omitempty"`
	Step      string    `json:"step"`
	// Args and Env are the agent's own, on its "start" entry.
	Args []string `json:"args,omitempty"`
	Env  []string `json:"env,omitempty"`
	// Child is the pid of the process a "grandchild" step started.
	Child int `json:"child,omitempty"`
	// Prompt is the prompt as the agent received it, on a "prompt" step.
	Prompt string `json:"prompt,omitempty"`
}

func newFakeWorker(dir string) (*fakeWorker, error) {
	if dir == "" {
		return nil, errors.New("no harness directory")
	}
	sc, err := readScenario(dir)
	if err != nil {
		return nil, err
	}
	w := &fakeWorker{dir: dir, sc: sc, replies: map[int64]int64{}}
	pgid, _ := syscall.Getpgid(0)
	// What this agent was started with, for the parent to check no task
	// token is in either.
	_ = appendJSONLine(filepath.Join(dir, agentLogFile), agentLogEntry{
		PID: os.Getpid(), PGID: pgid, StartedAt: time.Now(), Step: "start", Args: os.Args[1:], Env: os.Environ(),
	})
	return w, nil
}

func (w *fakeWorker) close() {
	if w.stopWatch != nil {
		for _, found := range w.stopWatch() {
			w.log(0, 0, "secret-file:"+found)
		}
	}
	if w.ledger != nil {
		_ = w.ledger.Close()
	}
}

func (w *fakeWorker) log(event int64, n int, step string) {
	pgid, _ := syscall.Getpgid(0)
	_ = appendJSONLine(filepath.Join(w.dir, agentLogFile),
		agentLogEntry{PID: os.Getpid(), PGID: pgid, StartedAt: time.Now(), Event: event, N: n, Step: step})
}

func (h *harness) agentLog() []agentLogEntry {
	var out []agentLogEntry
	_ = readJSONLines(filepath.Join(h.dir, agentLogFile), func(line []byte) error {
		var e agentLogEntry
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
		return nil
	})
	return out
}

// BadMode says this agent process reports a permission mode other than the
// one asked for.
func (w *fakeWorker) BadMode() bool {
	starts := 0
	_ = readJSONLines(filepath.Join(w.dir, agentLogFile), func(line []byte) error {
		var e agentLogEntry
		if json.Unmarshal(line, &e) == nil && e.Step == "start" {
			starts++
		}
		return nil
	})
	return starts <= w.sc.BadModeStarts
}

// Bind takes the worker's task from the MCP server declaration its driver
// handed the agent, exactly as `basecamp mcp --connect-state` does
// (internal/commands/mcp.go): the state directory is resolved by location and
// name, which is where the agent's id comes from; the token comes from the
// environment; and the ledger is opened as it is, never created and never
// migrated — the connector owns it.
func (w *fakeWorker) Bind(ctx context.Context, server driver.MCPServer) error {
	if server.Name != MCPServerName {
		return fmt.Errorf("the MCP server is %q, not %q", server.Name, MCPServerName)
	}
	i := slices.Index(server.Args, "--connect-state")
	if i < 0 || i+1 >= len(server.Args) {
		return errors.New("the MCP server has no --connect-state")
	}
	stateDir := server.Args[i+1]
	// The server resolves the directory against its own state home, which
	// is the environment the driver declared for it, not this agent's: a
	// declaration without one would send the real server elsewhere.
	home, ok := server.Env["XDG_STATE_HOME"]
	if !ok || home == "" {
		return errors.New("the MCP server's environment declares no XDG_STATE_HOME")
	}
	if err := os.Setenv("XDG_STATE_HOME", home); err != nil {
		return err
	}
	agentID, err := ResolveStateDir(stateDir, harnessAccount)
	if err != nil {
		return err
	}
	token := server.Env[TaskTokenEnv]
	if token == "" {
		return errors.New("the MCP server's environment carries no task token")
	}
	l, err := OpenExistingLedger(ctx, filepath.Join(stateDir, LedgerFile))
	if err != nil {
		return err
	}
	d, err := l.Dispatch(ctx, token, agentID)
	if err != nil {
		_ = l.Close()
		return err
	}
	w.ledger, w.dispatch = l, d
	return w.watchToken(token)
}

// watchToken keeps the task token where the parent test can read it back, and
// watches the working directories, for as long as this worker lives, for a
// file the token is written to. Whatever it finds is logged when the worker
// ends.
//
// Not the state directory: this process holds the ledger open, and reading
// the ledger's own files by another descriptor drops SQLite's POSIX locks on
// them, after which the connector's close can reset the WAL under this
// handle. The parent scans the state directory from a process of its own.
func (w *fakeWorker) watchToken(token string) error {
	tokens := filepath.Join(w.dir, tokensDir)
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(tokens, "task-*.token")
	if err != nil {
		return err
	}
	if _, err := f.WriteString(token); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	w.stopWatch = drivertest.WatchForSecretFiles(token, filepath.Join(w.dir, "work"), filepath.Join(w.dir, "work-other"))
	return nil
}

var promptEvent = regexp.MustCompile(`Event (\d+)`)

// Turn does what the scenario scripts for the prompt's event. An error is a
// step that could not be done; the agent reports the turn failed.
func (w *fakeWorker) Turn(ctx context.Context, prompt string) error {
	m := promptEvent.FindStringSubmatch(prompt)
	if m == nil {
		return errors.New("the prompt names no event")
	}
	event, _ := strconv.ParseInt(m[1], 10, 64)
	n := w.prompted(event) + 1
	pgid, _ := syscall.Getpgid(0)
	_ = appendJSONLine(filepath.Join(w.dir, agentLogFile),
		agentLogEntry{PID: os.Getpid(), PGID: pgid, StartedAt: time.Now(), Event: event, N: n, Step: "prompt", Prompt: prompt})
	steps, ok := w.sc.Plans[m[1]+"#"+strconv.Itoa(n)]
	if !ok {
		steps = []string{"get", "ack", "reply", "complete"}
	}
	for _, step := range steps {
		if err := w.step(ctx, event, n, step); err != nil {
			w.log(event, n, "error:"+step)
			return fmt.Errorf("%s: %w", step, err)
		}
		w.log(event, n, step)
	}
	return nil
}

func (w *fakeWorker) prompted(event int64) int {
	n := 0
	_ = readJSONLines(filepath.Join(w.dir, agentLogFile), func(line []byte) error {
		var e agentLogEntry
		if json.Unmarshal(line, &e) == nil && e.Event == event && e.Step == "prompt" {
			n++
		}
		return nil
	})
	return n
}

func (w *fakeWorker) step(ctx context.Context, event int64, n int, step string) error {
	if w.dispatch == nil {
		return errors.New("the worker was never bound to a task")
	}
	name, arg, _ := strings.Cut(step, ":")
	switch name {
	case "get":
		in, ok, err := w.dispatch.Get(ctx, event)
		if err != nil {
			return err
		}
		if !ok || in.EventID != event {
			return fmt.Errorf("get_dispatch did not return event %d", event)
		}
	case "ack":
		in, _, err := w.dispatch.Get(ctx, event)
		if err != nil {
			return err
		}
		id, err := postMessage(w.dir, storedMessage{Kind: MessageBoost, BucketID: in.Recording.BucketID, RecordingID: in.Recording.RecordingID, Content: "on it", By: "worker"})
		if err != nil {
			return err
		}
		if _, err := w.dispatch.Ack(ctx, event, &id); err != nil {
			return err
		}
	case "reply":
		in, _, err := w.dispatch.Get(ctx, event)
		if err != nil {
			return err
		}
		id, err := postMessage(w.dir, storedMessage{Kind: MessageComment, BucketID: in.Recording.BucketID, RecordingID: in.ReplyTo.RecordingID,
			Content: "done: event " + strconv.FormatInt(event, 10) + " attempt " + strconv.Itoa(n), By: "worker"})
		if err != nil {
			return err
		}
		w.replies[event] = id
	case "complete", "fail":
		outcome := OutcomeSucceeded
		if name == "fail" {
			outcome = OutcomeFailed
		}
		c := Completion{Outcome: outcome}
		if id, ok := w.replies[event]; ok {
			c.ReplyID = &id
		}
		if _, err := w.dispatch.Complete(ctx, event, c); err != nil {
			return err
		}
	case "kill":
		// The connector dies while this worker is mid-turn.
		return w.killConnector(ctx)
	case "linger":
		// A worker the connector left behind: it stays until something ends
		// its process group, which only the connector's restart may do.
		w.log(event, n, "linger")
		time.Sleep(2 * time.Minute)
		os.Exit(9)
	case "exit":
		code, _ := strconv.Atoi(arg)
		os.Exit(code)
	case "grandchild":
		// A process of the worker's own that outlives it, in its process
		// group, holding its working directory: the tree the one-owner rule
		// says nothing may be released around.
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		pid, err := syscall.ForkExec("/bin/sleep", []string{"sleep", "300"}, &syscall.ProcAttr{Dir: wd, Env: []string{}})
		if err != nil {
			return err
		}
		pgid, _ := syscall.Getpgid(0)
		return appendJSONLine(filepath.Join(w.dir, agentLogFile),
			agentLogEntry{PID: os.Getpid(), PGID: pgid, StartedAt: time.Now(), Event: event, N: n, Step: "grandchild", Child: pid})
	case "arrive":
		// A further event on the conversation while this one is in hand.
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return err
		}
		in, _, err := w.dispatch.Get(ctx, event)
		if err != nil {
			return err
		}
		return appendFeed(w.dir, feedEntry{Event: todoEvent(id, in.Recording.RecordingID)})
	case "await":
		// Wait for a record to reach a state: "await:<id>=<state>".
		id, state, _ := strings.Cut(arg, "=")
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return err
		}
		return waitFor(ctx, func() (bool, error) {
			r, ok, err := w.ledger.Get(ctx, n)
			return ok && slices.Contains(strings.Split(state, "|"), string(r.State)), err
		})
	default:
		return fmt.Errorf("unknown step %q", step)
	}
	return nil
}

// killConnector SIGKILLs the connector and returns once it is gone, so the
// steps after it run in a world without a connector.
//
// The connector is the pid it wrote down when it started, not this process's
// parent: a worker started behind an adapter (ACP) has the adapter as its
// parent, and an orphan's parent is the subreaper, which may be pid 1. A pid
// this harness did not record is never signaled.
func (w *fakeWorker) killConnector(ctx context.Context) error {
	running, err := harnessConnector(w.dir)
	if err != nil {
		return err
	}
	pid := running.PID
	// Only while it is still the process that wrote the file: a pid is not an
	// identity.
	if owns, err := driver.OwnsWorker(running); err != nil || !owns {
		if err == nil {
			err = errors.New("its start time no longer matches")
		}
		return fmt.Errorf("the connector's pid %d is no longer the connector: %w", pid, err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill the connector (pid %d): %w", pid, err)
	}
	return waitFor(ctx, func() (bool, error) { return processGone(ctx, pid), nil })
}

// harnessConnector reads the identity the connector wrote when it started.
func harnessConnector(dir string) (driver.Process, error) {
	data, err := os.ReadFile(filepath.Join(dir, connectorFile))
	if err != nil {
		return driver.Process{}, err
	}
	var running struct {
		PID       int       `json:"pid"`
		StartedAt time.Time `json:"started_at"`
	}
	if err := json.Unmarshal(data, &running); err != nil {
		return driver.Process{}, err
	}
	if running.PID <= 1 || running.StartedAt.IsZero() {
		return driver.Process{}, fmt.Errorf("the connector recorded pid %d, which is nothing this harness may signal", running.PID)
	}
	// The group is only asked about when the process is gone; the kill is of
	// the pid alone.
	return driver.Process{PID: running.PID, PGID: running.PID, StartedAt: running.StartedAt}, nil
}

func waitFor(ctx context.Context, cond func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		ok, err := cond()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
