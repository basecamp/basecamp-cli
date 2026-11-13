package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector"
)

// Activity is the runner's work as someone watching reads it: what is being
// done now, what was done, and for which request. It is read from the state
// file, so it answers whether or not a connector is running.
type Activity struct {
	Orchestrator Spec `json:"orchestrator"`
	// Turns counts the orchestrator's finished turns, Waiting the messages
	// it has not handled yet, and SetAside those it gave up on.
	Turns    int `json:"turns"`
	Waiting  int `json:"waiting"`
	SetAside int `json:"set_aside"`
	// Process is the process group the orchestrator's session still names:
	// its turn in flight, or what a turn that ended left running. Zero
	// when it names none.
	Process int `json:"process,omitempty"`
	// Workers are newest first.
	Workers []WorkerActivity `json:"workers"`
}

// WorkerActivity is a worker and the request it was given last.
type WorkerActivity struct {
	WorkerView
	Request *RequestSummary `json:"request,omitempty"`
	// Process is the process group the worker's session still names, as
	// Activity.Process is the orchestrator's. On a worker that is not
	// running it is what its last turn left, which keeps the session from
	// another turn until it is gone.
	Process int `json:"process,omitempty"`
}

// RequestSummary is where a request was made and by whom. Title is the
// recording's own: a card's or a to-do's name, the subject of a message, and
// for a chat line, which has no other, the start of the line itself. The
// body of what was asked is not carried.
type RequestSummary struct {
	Project     int64     `json:"project,omitempty"`
	ProjectName string    `json:"project_name,omitempty"`
	Ping        bool      `json:"ping,omitempty"`
	Type        string    `json:"type,omitempty"`
	Title       string    `json:"title,omitempty"`
	URL         string    `json:"url,omitempty"`
	Trigger     string    `json:"trigger,omitempty"`
	Requester   string    `json:"requester,omitempty"`
	At          time.Time `json:"at"`
}

// ReadActivity reads the activity kept in dir, the runner's state directory.
// It reports false when no runner has kept anything there. It takes no lock
// and changes nothing: the state file is replaced whole on every save, so a
// read beside a running connector sees one save or the next.
func ReadActivity(dir string) (Activity, bool, error) {
	path := filepath.Join(dir, StateFile)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Activity{}, false, nil
	}
	st, err := loadState(path)
	if err != nil {
		return Activity{}, false, err
	}
	activity := Activity{
		Orchestrator: st.Orchestrator.Spec, Turns: st.Orchestrator.Turns,
		Waiting: len(st.Inbox), SetAside: len(st.Dropped),
		Workers: make([]WorkerActivity, 0, len(st.Workers)),
	}
	if left := st.Orchestrator.Process; left != nil {
		activity.Process = left.PGID
	}
	for _, w := range slices.Backward(st.Workers) {
		worker := WorkerActivity{WorkerView: viewOf(w)}
		if left := w.Session.Process; left != nil {
			worker.Process = left.PGID
		}
		if req, ok := st.Requests[w.EventID]; ok {
			worker.Request = summarize(req)
		}
		activity.Workers = append(activity.Workers, worker)
	}
	return activity, true, nil
}

// maxSummaryTitle bounds the title a summary carries.
const maxSummaryTitle = 140

// summarize is the part of a kept request someone watching may be shown.
func summarize(req request) *RequestSummary {
	summary := &RequestSummary{Project: req.Project, Ping: req.Ping, Type: req.RecordingType, At: req.At}
	var line connector.HandoffLine
	if json.Unmarshal([]byte(req.Line), &line) == nil {
		summary.ProjectName = line.Recording.ProjectName
		summary.Title = clip(line.Recording.Title, maxSummaryTitle)
		summary.URL = line.Recording.URL
		summary.Trigger = line.Trigger
		summary.Requester = line.RequesterName
	}
	return summary
}
