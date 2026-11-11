package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// DispatchRequest asks for a worker: `basecamp connect dispatch`.
type DispatchRequest struct {
	// EventID is the request the worker is for. Zero is work the owner asked
	// for at this computer, which no request line carries.
	EventID int64 `json:"event_id,omitempty"`
	// Repo is the directory the worker starts in; empty with NoRepo.
	Repo   string `json:"repo,omitempty"`
	NoRepo bool   `json:"no_repo,omitempty"`
	// Spec is what the dispatch itself asked for, over every rule.
	Spec Spec `json:"spec,omitzero"`
	// Handoff is everything the worker is told.
	Handoff string `json:"handoff"`
	// Again starts a new worker for an event that already has one.
	Again bool `json:"again,omitempty"`
}

// WorkerView is a worker as `basecamp connect workers list` shows it.
type WorkerView struct {
	ID      string `json:"id"`
	EventID int64  `json:"event_id,omitempty"`
	State   string `json:"state"`
	Adapter string `json:"adapter"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Repo    string `json:"repo,omitempty"`
	// Matched is the runner.json rule that chose the agent, when one did.
	Matched string `json:"matched,omitempty"`
	// Requests counts the requests it has been given: the first, and each
	// later one on the same thread.
	Requests int `json:"requests"`
	// Doing is the tool a running worker called last.
	Doing string `json:"doing,omitempty"`
	// Refused counts the tool calls its coding agent refused, by a rule of
	// its own settings.
	Refused   int        `json:"refused,omitempty"`
	Report    string     `json:"report,omitempty"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	CostUSD   float64    `json:"cost_usd,omitempty"`
}

// view is w as a WorkerView. r.mu is held.
func (r *Runner) view(w *workerState) WorkerView {
	v := WorkerView{
		ID: w.ID, EventID: w.EventID, State: w.State,
		Adapter: w.Session.Spec.Adapter, Model: w.Session.Spec.Model, Effort: w.Session.Spec.Effort,
		Repo: w.Repo, Matched: w.Matched, Requests: max(1, len(w.Events)), Refused: w.Refused, Report: clip(w.Report, 600), Error: w.Error,
		CreatedAt: w.CreatedAt, CostUSD: roundCost(w.Session.CostUSD),
	}
	if w.State == workerRunning {
		v.Doing = r.doing[w.ID]
	}
	// Copies, not pointers into the worker: a view is read after the lock is
	// let go, while the worker's goroutine may be writing its times.
	if !w.StartedAt.IsZero() {
		started := w.StartedAt
		v.StartedAt = &started
	}
	if !w.EndedAt.IsZero() {
		ended := w.EndedAt
		v.EndedAt = &ended
	}
	return v
}

// describeWorker names a worker and the request of its that is spoken of.
func describeWorker(w *workerState, eventID int64) string {
	what := fmt.Sprintf("worker %s", w.ID)
	if eventID != 0 {
		what += fmt.Sprintf(" for event %d", eventID)
	}
	where := "no repo"
	if w.Repo != "" {
		where = w.Repo
	}
	return fmt.Sprintf("%s (%s, %s)", what, w.Session.Spec.String(), where)
}

// config is runner.json as it is now.
func (r *Runner) config() (Config, error) { return LoadConfig(r.opts.ConfigPath) }

// baseWorker is the worker a request runs on when no rule says otherwise:
// what the connector was started with, over runner.json's worker, over the
// orchestrator's own agent. r.mu is held.
func (r *Runner) baseWorker(config Config) Spec {
	return r.opts.Worker.Over(config.Worker.Over(r.st.Orchestrator.Spec))
}

// Dispatch gives a request to a worker and returns at once. A request that
// already has a worker gets that worker back, with existing true, unless the
// dispatch asks for another.
//
// A request made on a thread an earlier worker handled goes to that worker,
// continued in its session, so what it did before is in front of it: the
// worktree, the branch, what it found. That needs the earlier work to have
// been in the same repo and on the same coding agent, since a session lives
// in the directory and the CLI it was started in. Without either, and with
// --again, a new worker starts. A session that turns out not to open any
// more is replaced by a new one too, when its turn comes (work).
func (r *Runner) Dispatch(ctx context.Context, req DispatchRequest) (view WorkerView, existing bool, err error) {
	if req.Handoff == "" {
		return WorkerView{}, false, errors.New("the handoff is empty: give the worker everything it needs on standard input")
	}
	if (req.Repo == "") != req.NoRepo {
		return WorkerView{}, false, errors.New("say where the work goes: --repo <path>, or --no-repo for work that needs none")
	}
	if err := req.Spec.Validate(true); err != nil {
		return WorkerView{}, false, err
	}
	repo := ""
	if req.Repo != "" {
		home, _ := os.UserHomeDir()
		repo = filepath.Clean(expandHome(req.Repo, home))
		if !filepath.IsAbs(repo) {
			return WorkerView{}, false, fmt.Errorf("--repo %q is not an absolute path", req.Repo)
		}
		if info, err := os.Stat(repo); err != nil || !info.IsDir() {
			return WorkerView{}, false, fmt.Errorf("--repo %q is not a directory on this computer", req.Repo)
		}
	}
	// A rules file that cannot be read is said, never guessed around: the
	// request would otherwise run on an agent its rule did not choose.
	config, err := r.config()
	if err != nil {
		return WorkerView{}, false, err
	}

	r.mu.Lock()
	if r.ctx == nil {
		r.mu.Unlock()
		return WorkerView{}, false, errNotStarted
	}
	request, known := r.st.Requests[req.EventID]
	if req.EventID != 0 && !known {
		r.mu.Unlock()
		return WorkerView{}, false, fmt.Errorf("event %d is not a request this connector handed off", req.EventID)
	}
	if w := r.st.workerFor(req.EventID); req.EventID != 0 && w != nil && !req.Again {
		defer r.mu.Unlock()
		return r.view(w), true, nil
	}
	recordings, cached := r.recordings[req.EventID]
	r.mu.Unlock()

	route := Route{Repo: repo, Project: request.Project, Ping: request.Ping}
	fetched := false
	if known && !request.Ping && config.NeedsRecordings() {
		if !cached && r.opts.Recordings != nil {
			recordings, fetched = r.opts.Recordings(ctx, request.Project, request.RecordingID, request.RecordingType), true
		}
		route.Recordings = recordings
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// The lock was let go for that read, and another dispatch for the same
	// event may have got here first: it is asked again before anything is
	// added, so one request never gets two workers by a race.
	if w := r.st.workerFor(req.EventID); req.EventID != 0 && w != nil && !req.Again {
		return r.view(w), true, nil
	}
	// Kept only when it was read: an event dispatched while no rule asked
	// what it sits under is read when a rule first does.
	if fetched {
		r.recordings[req.EventID] = recordings
	}
	home, _ := os.UserHomeDir()
	spec, matched := config.WorkerFor(r.baseWorker(config), route, home)
	spec = req.Spec.Over(spec)
	if err := spec.Validate(false); err != nil {
		return WorkerView{}, false, err
	}
	if err := CheckPermissions(config.Mode(), spec); err != nil {
		return WorkerView{}, false, err
	}
	guarded := guardedNote(config.Mode(), spec.Adapter)
	handoff := req.Handoff
	if known && request.Line != "" {
		// The request line is the connector's to give: it has it whole, and
		// the orchestrator need not carry JSON through a shell to pass it on.
		handoff = "The request line, as the connector handed it off:\n" + request.Line + "\n\n" + handoff
	}

	if earlier := r.st.workerOn(request.Thread); known && !req.Again && earlier != nil && earlier.Repo == repo &&
		earlier.Session.Spec.Adapter == spec.Adapter && (earlier.live() || earlier.Session.ID != "") {
		// A model or an effort may have changed since; the session takes it
		// from its next turn.
		earlier.Session.Spec, earlier.Matched = spec, matched
		earlier.Prompts = append(earlier.Prompts, prompt{
			Text:    furtherRequest(r.workerDoc(earlier.ID), guarded) + "\n\n" + handoff,
			Fresh:   workerInstructions(r.opts.Subagent, repo, earlier.Session.Dir, guarded) + "\n\n" + handoff,
			EventID: req.EventID,
		})
		earlier.Events, earlier.EventID = append(earlier.Events, req.EventID), req.EventID
		idle := !earlier.live()
		if idle {
			// Its times are those of the request it is on now.
			earlier.State, earlier.Error, earlier.StartedAt, earlier.EndedAt = workerQueued, "", time.Time{}, time.Time{}
		}
		if err := r.st.save(r.statePath()); err != nil {
			return WorkerView{}, false, err
		}
		r.log.Info("runner: request given to the worker that handled its thread before", "worker", earlier.ID, "event_id", req.EventID, "agent", spec.String(), "session", cmpText(earlier.Session.ID, "none yet"))
		if idle {
			r.start(earlier.ID) //nolint:contextcheck // a worker outlives the dispatch that starts it: it runs under the run's context
		}
		return r.view(earlier), false, nil
	}

	r.st.WorkerSeq++
	w := &workerState{
		ID: fmt.Sprintf("w%d", r.st.WorkerSeq), EventID: req.EventID, Events: []int64{req.EventID}, Thread: request.Thread, Repo: repo,
		Session: sessionState{Spec: spec, Dir: repo}, Matched: matched,
		State: workerQueued, CreatedAt: r.opts.Now(),
	}
	if repo == "" {
		w.Session.Dir = filepath.Join(r.opts.Dir, "work", w.ID)
		if err := os.MkdirAll(w.Session.Dir, 0o700); err != nil {
			return WorkerView{}, false, err
		}
	}
	instructions := workerInstructions(r.opts.Subagent, repo, w.Session.Dir, guarded)
	// Kept for the worker to read again in a session long enough to have
	// lost them.
	if err := os.WriteFile(r.workerDoc(w.ID), []byte(instructions), 0o600); err != nil {
		return WorkerView{}, false, err
	}
	first := instructions + "\n\n" + handoff
	w.Prompts = []prompt{{Text: first, Fresh: first, EventID: req.EventID}}
	r.st.Workers = append(r.st.Workers, w)
	if err := r.st.save(r.statePath()); err != nil {
		r.st.Workers = r.st.Workers[:len(r.st.Workers)-1]
		return WorkerView{}, false, err
	}
	r.log.Info("runner: worker dispatched", "worker", w.ID, "event_id", w.EventID, "agent", spec.String(), "repo", cmpText(repo, "none"), "matched", cmpText(matched, "the default worker"))
	r.start(w.ID) //nolint:contextcheck // a worker outlives the dispatch that starts it: it runs under the run's context
	return r.view(w), false, nil
}

// workerDoc is where a worker's instructions are kept.
func (r *Runner) workerDoc(id string) string {
	return filepath.Join(r.opts.Dir, "instructions", id+".md")
}

// errNotStarted is a worker asked for before Run has taken up the workers
// the last stop left: starting one then would start it twice.
var errNotStarted = errors.New("the connector's runner has not started yet")

// start runs a worker's turns on a goroutine of its own, unless one already
// is: a worker told something more while its goroutine is still about takes
// it up there. r.mu is held.
func (r *Runner) start(id string) {
	if r.working[id] {
		return
	}
	r.working[id] = true
	r.wg.Go(func() { r.work(id) })
}

// continueWorkers starts again every worker a stop left unfinished. One that
// was mid-turn is continued in its session and told it was interrupted; one
// whose session never opened starts over from its handoff. r.mu is held.
func (r *Runner) continueWorkers() {
	for _, w := range r.st.Workers {
		if !w.live() {
			continue
		}
		if w.State == workerRunning && w.Session.ID != "" && len(w.Prompts) > 0 {
			w.Prompts[0].Text = interruptedNote
		}
		w.State = workerQueued
		r.log.Info("runner: continuing a worker the last stop interrupted", "worker", w.ID, "event_id", w.EventID, "session", cmpText(w.Session.ID, "none yet"))
		r.start(w.ID)
	}
}

// Send tells a worker something more. A worker that is running hears it
// after its turn; one that has finished is continued in its session.
func (r *Runner) Send(id, message string) (WorkerView, error) {
	if message == "" {
		return WorkerView{}, errors.New("the message is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx == nil {
		return WorkerView{}, errNotStarted
	}
	w := r.st.worker(id)
	if w == nil {
		return WorkerView{}, fmt.Errorf("there is no worker %q", id)
	}
	if !w.live() && w.Session.ID == "" {
		return WorkerView{}, fmt.Errorf("worker %s never opened a session to continue: dispatch its request again with --again", id)
	}
	w.Prompts = append(w.Prompts, prompt{Text: "[orchestrator] " + message, EventID: w.EventID})
	idle := !w.live()
	if idle {
		w.State, w.Error, w.StartedAt, w.EndedAt = workerQueued, "", time.Time{}, time.Time{}
	}
	if err := r.st.save(r.statePath()); err != nil {
		return WorkerView{}, err
	}
	if idle {
		r.start(id)
	}
	return r.view(w), nil
}

// StopWorker ends a worker: its running turn is stopped and nothing more is
// sent to it.
func (r *Runner) StopWorker(id string) (WorkerView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.st.worker(id)
	if w == nil {
		return WorkerView{}, fmt.Errorf("there is no worker %q", id)
	}
	if w.live() {
		w.State, w.EndedAt, w.Prompts = workerStopped, r.opts.Now(), nil
		if cancel, ok := r.turns[id]; ok {
			// Its goroutine learns the turn was stopped from this, not from
			// the worker's state, which a message sent before the turn has
			// ended may already have changed.
			r.stopping[id] = true
			cancel()
		}
		if err := r.st.save(r.statePath()); err != nil {
			return WorkerView{}, err
		}
		r.log.Info("runner: worker stopped", "worker", id)
	}
	return r.view(w), nil
}

// Workers lists the workers the state still holds, oldest first.
func (r *Runner) Workers() []WorkerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	views := make([]WorkerView, 0, len(r.st.Workers))
	for _, w := range r.st.Workers {
		views = append(views, r.view(w))
	}
	return views
}

// work is a worker's one goroutine. It runs the worker's turns, and before
// it goes it looks once more, under the lock that start takes, for something
// the worker was told while it was on its way out.
func (r *Runner) work(id string) {
	for {
		r.workTurns(id)
		r.mu.Lock()
		w := r.st.worker(id)
		more := r.ctx.Err() == nil && w != nil && w.live() && len(w.Prompts) > 0
		if !more {
			delete(r.working, id)
		}
		r.mu.Unlock()
		if !more {
			return
		}
	}
}

// workTurns runs a worker's turns until it has nothing more to be told, and
// reports each to the orchestrator. Only so many workers run at once; the
// rest wait their turn here.
func (r *Runner) workTurns(id string) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-r.ctx.Done():
		return
	}
	for {
		r.mu.Lock()
		w := r.st.worker(id)
		if w == nil || !w.live() || len(w.Prompts) == 0 {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		if r.released(r.ctx) != nil {
			return
		}
		mode, err := r.permissions(r.ctx)
		if err != nil {
			return
		}
		r.mu.Lock()
		if w = r.st.worker(id); w == nil || !w.live() || len(w.Prompts) == 0 {
			r.mu.Unlock()
			return
		}
		if err := CheckPermissions(mode, w.Session.Spec); err != nil {
			r.finish(w, workerFailed, err.Error())
			r.mu.Unlock()
			return
		}
		next, session := w.Prompts[0], w.Session
		w.State = workerRunning
		if w.StartedAt.IsZero() {
			w.StartedAt = r.opts.Now()
		}
		adapter := adapterByName(session.Spec.Adapter)
		turnCtx, cancel := context.WithTimeout(r.ctx, r.opts.WorkerTimeout)
		r.turns[id] = cancel
		err = r.st.save(r.statePath())
		r.mu.Unlock()
		if err != nil {
			cancel()
			r.log.Error("runner: a worker's state could not be saved; it is not started", "worker", id, "error", err)
			r.mu.Lock()
			delete(r.turns, id)
			if w := r.st.worker(id); w != nil {
				r.finish(w, workerFailed, "its state could not be saved")
			}
			r.mu.Unlock()
			return
		}

		turn := Turn{Role: RoleWorker, Spec: session.Spec, Resume: session.ID, Dir: session.Dir, Permissions: mode}
		if session.ID == "" {
			turn.NewID = adapter.NewSessionID()
		}
		result, err := r.turn(turnCtx, id, adapter, turn, next.Text, func(process driver.Process) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if w := r.st.worker(id); w != nil {
				w.Session.Process = &process
				_ = r.st.save(r.statePath())
			}
		}, func(event Event) {
			r.mu.Lock()
			defer r.mu.Unlock()
			switch event.Kind {
			case EventSession:
				if w := r.st.worker(id); w != nil && event.SessionID != "" && w.Session.ID != event.SessionID {
					w.Session.ID = event.SessionID
					_ = r.st.save(r.statePath())
				}
			case EventTool:
				r.doing[id] = event.Tool
			case EventText, EventResult, EventError, EventUsage:
			}
		})
		timedOut := errors.Is(turnCtx.Err(), context.DeadlineExceeded)
		cancel()

		r.mu.Lock()
		delete(r.turns, id)
		delete(r.doing, id)
		w = r.st.worker(id)
		if w == nil {
			r.mu.Unlock()
			return
		}
		stopped := r.stopping[id]
		delete(r.stopping, id)
		w.Session.charge(result)
		w.Session.Process = nil
		w.Refused += len(result.Refused)
		if len(result.Refused) > 0 {
			r.log.Warn("runner: a worker's coding agent refused some of what it tried, by a rule of its own settings", "worker", id, "refused", describeRefused(result.Refused))
		}
		if result.SessionID != "" {
			w.Session.ID = result.SessionID
		}
		switch {
		case r.ctx.Err() != nil:
			// The connector is stopping. The worker stays as it is, running,
			// and is continued at the next start.
			_ = r.st.save(r.statePath())
			r.mu.Unlock()
			return
		case stopped:
			// The turn was stopped. Whether the worker has been told
			// anything since is for the top of the loop to see.
			_ = r.st.save(r.statePath())
			r.mu.Unlock()
			continue
		case timedOut:
			r.finish(w, workerFailed, fmt.Sprintf("it ran longer than %s and was stopped", r.opts.WorkerTimeout))
		case err != nil && turn.Resume != "" && result.SessionID == "" && next.Fresh != "":
			// The CLI never opened the session it was asked to continue: it
			// is gone, or cannot be read any more. The request starts over in
			// a new one, with the instructions whole, rather than failing for
			// want of the old.
			r.log.Warn("runner: a worker's session could not be continued; its request starts in a new one", "worker", id, "session", turn.Resume, "error", err)
			w.Session.ID, w.Session.Turns = "", 0
			w.Prompts[0].Text, w.Prompts[0].Fresh = next.Fresh, ""
			w.State = workerQueued
			_ = r.st.save(r.statePath())
			r.mu.Unlock()
			continue
		case err != nil:
			w.Session.Failures++
			if w.Session.Failures < 2 {
				// Once more: in the session the CLI opened, where it can see
				// what it already did, or with the same words when it opened
				// none.
				r.log.Warn("runner: a worker's turn failed; it is tried once more", "worker", id, "error", err)
				if result.SessionID != "" {
					w.Prompts[0].Text = "[connector] Your last turn ended in an error before you finished (" + clip(richtext.SanitizeSingleLine(err.Error()), 300) +
						"). You are continued in the same session. Check what you had already done, do not repeat a post or a push that landed, finish the request, and end with your one-line report."
				}
				w.State = workerQueued
				_ = r.st.save(r.statePath())
				r.mu.Unlock()
				continue
			}
			r.finish(w, workerFailed, richtext.SanitizeSingleLine(err.Error()))
		default:
			w.Session.Failures = 0
			w.Session.Turns++
			w.Prompts = w.Prompts[1:]
			w.Report = result.Text
			r.report(w, next.EventID, len(result.Refused), "")
			if len(w.Prompts) > 0 {
				// Queued, not running: what is first in Prompts has not been
				// sent, which is what a restart reads the state for.
				w.State = workerQueued
				_ = r.st.save(r.statePath())
				r.mu.Unlock()
				r.signal()
				continue
			}
			r.finish(w, workerDone, "")
		}
		r.mu.Unlock()
		return
	}
}

// report tells the orchestrator how a worker's turn for a request ended.
// r.mu is held.
func (r *Runner) report(w *workerState, eventID int64, refused int, failure string) {
	body := describeWorker(w, eventID) + " finished. Its report:\n" + clip(cmpText(w.Report, "(it said nothing)"), maxReport)
	if refused > 0 {
		body += fmt.Sprintf("\n(Its coding agent refused %d of the commands it tried, by a rule of the owner's own settings, so its work may be incomplete.)", refused)
	}
	if failure != "" {
		body = describeWorker(w, eventID) + " could not run: " + failure + ". Nobody has replied to its request."
	}
	r.addItem(kindReport, eventID, body)
}

// finish closes a worker. One that failed leaves every request it still
// had to answer unanswered, and the orchestrator is told of each. r.mu is
// held.
func (r *Runner) finish(w *workerState, state, failure string) {
	if state == workerFailed {
		var told []int64
		for _, p := range w.Prompts {
			if !slices.Contains(told, p.EventID) {
				told = append(told, p.EventID)
				r.report(w, p.EventID, 0, failure)
			}
		}
	}
	w.State, w.Error, w.EndedAt, w.Prompts = state, failure, r.opts.Now(), nil
	if err := r.st.save(r.statePath()); err != nil {
		r.log.Error("runner: a worker's end could not be saved", "worker", w.ID, "error", err)
	}
	r.log.Info("runner: worker "+state, "worker", w.ID, "event_id", w.EventID, "agent", w.Session.Spec.String(),
		"took", w.EndedAt.Sub(w.StartedAt).Round(time.Second), "cost_usd", roundCost(w.Session.CostUSD), "error", failure)
	r.signal()
}
