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
	ppid     int

	replies map[int64]int64
}

// agentLogEntry is one thing a fake agent did.
type agentLogEntry struct {
	PID   int    `json:"pid"`
	PGID  int    `json:"pgid"`
	Event int64  `json:"event,omitempty"`
	N     int    `json:"n,omitempty"`
	Step  string `json:"step"`
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
	w := &fakeWorker{dir: dir, sc: sc, ppid: os.Getppid(), replies: map[int64]int64{}}
	w.log(0, 0, "start")
	return w, nil
}

func (w *fakeWorker) close() {
	if w.ledger != nil {
		_ = w.ledger.Close()
	}
}

func (w *fakeWorker) log(event int64, n int, step string) {
	pgid, _ := syscall.Getpgid(0)
	_ = appendJSONLine(filepath.Join(w.dir, agentLogFile), agentLogEntry{PID: os.Getpid(), PGID: pgid, Event: event, N: n, Step: step})
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
// handed the agent: the state directory in its arguments and the token in its
// environment, as `basecamp mcp --connect-state` reads them.
func (w *fakeWorker) Bind(server driver.MCPServer) error {
	if server.Name != MCPServerName {
		return fmt.Errorf("the MCP server is %q, not %q", server.Name, MCPServerName)
	}
	i := slices.Index(server.Args, "--connect-state")
	if i < 0 || i+1 >= len(server.Args) {
		return errors.New("the MCP server has no --connect-state")
	}
	token := server.Env[TaskTokenEnv]
	if token == "" {
		return errors.New("the MCP server's environment carries no task token")
	}
	l, err := OpenLedger(filepath.Join(server.Args[i+1], LedgerFile))
	if err != nil {
		return err
	}
	d, err := l.Dispatch(token, harnessAgent)
	if err != nil {
		_ = l.Close()
		return err
	}
	w.ledger, w.dispatch = l, d
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
	_ = appendJSONLine(filepath.Join(w.dir, agentLogFile), agentLogEntry{PID: os.Getpid(), PGID: pgid, Event: event, N: n, Step: "prompt", Prompt: prompt})
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
		w.killConnector(ctx)
	case "linger":
		// A worker the connector left behind: it stays until something ends
		// its process group, which only the connector's restart may do.
		w.log(event, n, "linger")
		time.Sleep(2 * time.Minute)
		os.Exit(9)
	case "exit":
		code, _ := strconv.Atoi(arg)
		os.Exit(code)
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

// killConnector SIGKILLs the process that started this worker and returns once
// it is gone: the kernel reparents an orphan, so a changed parent is proof.
func (w *fakeWorker) killConnector(ctx context.Context) {
	_ = syscall.Kill(w.ppid, syscall.SIGKILL)
	_ = waitFor(ctx, func() (bool, error) { return os.Getppid() != w.ppid, nil })
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
