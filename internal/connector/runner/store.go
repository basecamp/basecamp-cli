package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// StateFile is the runner's state, in its directory.
const StateFile = "state.json"

// stateVersion is the state file's schema version.
const stateVersion = 1

// state is everything the runner must still know after a restart.
type state struct {
	Version int `json:"version"`
	// Seq numbers inbox items, and WorkerSeq workers.
	Seq       int64 `json:"seq"`
	WorkerSeq int64 `json:"worker_seq"`
	// Orchestrator is the orchestrator's session.
	Orchestrator sessionState `json:"orchestrator"`
	// Inbox holds what the orchestrator has not yet handled, oldest first.
	Inbox []item `json:"inbox"`
	// Dropped holds what it failed to handle after every retry, for a person
	// to find.
	Dropped []item `json:"dropped,omitempty"`
	// Requests are the request lines delivered, by event id: what tells a
	// repeat from a new request, and what a dispatch routes by.
	Requests map[int64]request `json:"requests"`
	Workers  []*workerState    `json:"workers"`
}

// sessionState is one headless session.
type sessionState struct {
	Spec Spec `json:"spec"`
	// ID is the session the CLI confirmed. Empty until it has.
	ID string `json:"id,omitempty"`
	// Dir is the directory it was started in, where it is continued.
	Dir string `json:"dir,omitempty"`
	// Turns counts the turns it has finished.
	Turns int `json:"turns,omitempty"`
	// Failures counts consecutive failed turns.
	Failures int `json:"failures,omitempty"`
	// CostUSD is what the session has cost, as far as its CLI says.
	CostUSD float64 `json:"cost_usd,omitempty"`
	// Process is the CLI process of the turn in flight, and nil between
	// turns. A run that finds one at its start did not end it: the run
	// before crashed, and the process may still be working.
	Process *driver.Process `json:"process,omitempty"`
}

// charge adds a turn to what the session has cost and returns the turn's
// own share. A CLI that reports the session's running total has the total
// taken as it stands.
func (s *sessionState) charge(result TurnResult) float64 {
	if result.SessionCostUSD > 0 {
		turn := result.SessionCostUSD - s.CostUSD
		if turn < 0 {
			turn = result.SessionCostUSD
		}
		s.CostUSD = result.SessionCostUSD
		return turn
	}
	s.CostUSD += result.Usage.CostUSD
	return result.Usage.CostUSD
}

// Kinds of inbox item.
const (
	kindRequest   = "request"
	kindReport    = "report"
	kindOwner     = "owner"
	kindConnector = "connector"
)

// item is one message for the orchestrator.
type item struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	EventID int64     `json:"event_id,omitempty"`
	Body    string    `json:"body"`
	At      time.Time `json:"at"`
	// Attempts counts the turns that failed with this item in them.
	Attempts int `json:"attempts,omitempty"`
}

// request is what the runner keeps of a request line.
type request struct {
	Project       int64     `json:"project,omitempty"`
	Ping          bool      `json:"ping,omitempty"`
	RecordingID   int64     `json:"recording_id"`
	RecordingType string    `json:"recording_type,omitempty"`
	At            time.Time `json:"at"`
	// Line is the request line whole, which a dispatch for the event gives
	// the worker.
	Line string `json:"line,omitempty"`
	// Thread is where the request's answer goes: the card, to-do, message or
	// chat it was made on. A later request on the same thread is given to
	// the worker that handled this one.
	Thread string `json:"thread,omitempty"`
}

// prompt is one thing a worker has still to be told.
type prompt struct {
	// Text is what its session is sent.
	Text string `json:"text"`
	// Fresh is what a new session is sent in Text's place when the worker's
	// own session cannot be opened again: the instructions and the request,
	// whole. Empty for a message that means nothing without the session.
	Fresh string `json:"fresh,omitempty"`
	// EventID is the request the prompt is for, which its turn's report
	// names.
	EventID int64 `json:"event_id,omitempty"`
}

// UnmarshalJSON reads a prompt, and a bare string as the text of one: what
// a state file written before prompts carried more than text holds.
func (p *prompt) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		*p = prompt{Text: text}
		return nil
	}
	type plain prompt
	return json.Unmarshal(data, (*plain)(p))
}

// Worker states.
const (
	workerQueued  = "queued"
	workerRunning = "running"
	workerDone    = "done"
	workerFailed  = "failed"
	workerStopped = "stopped"
)

// workerState is one worker: a session of its own doing one request, and
// the later requests made on the same thread.
type workerState struct {
	ID string `json:"id"`
	// EventID is the request it was given last, and Events every request it
	// has been given, oldest first.
	EventID int64   `json:"event_id,omitempty"`
	Events  []int64 `json:"events,omitempty"`
	// Thread is the thread its requests were made on.
	Thread  string       `json:"thread,omitempty"`
	Repo    string       `json:"repo,omitempty"`
	Session sessionState `json:"session"`
	// Matched is the runner.json rule that chose Session.Spec, when one did.
	Matched string `json:"matched,omitempty"`
	State   string `json:"state"`
	// Prompts are what the worker has still to be told, oldest first: its
	// handoff, then anything sent to it since.
	Prompts []prompt `json:"prompts,omitempty"`
	// Refused counts the tool calls its CLI refused.
	Refused int `json:"refused,omitempty"`
	// Report is what it answered last.
	Report    string    `json:"report,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

func (w *workerState) live() bool { return w.State == workerQueued || w.State == workerRunning }

// Bounds on what the state file keeps.
const (
	keepRequests = 500
	keepWorkers  = 100
	keepDropped  = 50
)

// loadState reads the state file. A missing one is a fresh state.
func loadState(path string) (*state, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the connector's own state directory
	if errors.Is(err, os.ErrNotExist) {
		return &state{Version: stateVersion, Requests: map[int64]request{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s cannot be read: %w", path, err)
	}
	if s.Version != stateVersion {
		return nil, fmt.Errorf("%s is version %d, and this build reads version %d", path, s.Version, stateVersion)
	}
	if s.Requests == nil {
		s.Requests = map[int64]request{}
	}
	return &s, nil
}

// save writes the state file whole, atomically and owner-only, after
// dropping what is past its bounds.
func (s *state) save(path string) error {
	s.prune()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// prune drops the oldest requests, finished workers and dropped items past
// what the file keeps. A live worker is never dropped.
func (s *state) prune() {
	if over := len(s.Requests) - keepRequests; over > 0 {
		ids := make([]int64, 0, len(s.Requests))
		for id := range s.Requests {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids[:over] {
			delete(s.Requests, id)
		}
	}
	if over := len(s.Workers) - keepWorkers; over > 0 {
		s.Workers = slices.DeleteFunc(s.Workers, func(w *workerState) bool {
			if over > 0 && !w.live() {
				over--
				return true
			}
			return false
		})
	}
	if over := len(s.Dropped) - keepDropped; over > 0 {
		s.Dropped = s.Dropped[over:]
	}
}

func (s *state) worker(id string) *workerState {
	i := slices.IndexFunc(s.Workers, func(w *workerState) bool { return w.ID == id })
	if i < 0 {
		return nil
	}
	return s.Workers[i]
}

// workerFor is the latest worker given an event.
func (s *state) workerFor(eventID int64) *workerState {
	for i := len(s.Workers) - 1; i >= 0; i-- {
		if w := s.Workers[i]; w.EventID == eventID || slices.Contains(w.Events, eventID) {
			return w
		}
	}
	return nil
}

// workerOn is the latest worker whose requests were made on thread.
func (s *state) workerOn(thread string) *workerState {
	for i := len(s.Workers) - 1; i >= 0 && thread != ""; i-- {
		if s.Workers[i].Thread == thread {
			return s.Workers[i]
		}
	}
	return nil
}
