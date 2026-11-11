package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// Defaults a Config or an Options leaves unset.
const (
	// DefaultMaxWorkers is how many workers run at once.
	DefaultMaxWorkers = 4
	// DefaultOrchestratorTimeout bounds one orchestrator turn: it
	// acknowledges and hands off, and never does the work.
	DefaultOrchestratorTimeout = 15 * time.Minute
	// DefaultWorkerTimeout bounds one worker turn.
	DefaultWorkerTimeout = 3 * time.Hour
)

const (
	// maxBatch is how many messages one orchestrator turn carries.
	maxBatch = 8
	// maxAttempts is how many turns may fail with a message in them before
	// it is set aside.
	maxAttempts = 4
	// maxReport is how much of a worker's answer the orchestrator is given.
	maxReport = 4000
)

// Options configure a Runner.
type Options struct {
	// Profile is the agent's CLI profile.
	Profile string
	// Dir is the runner's own directory, inside the connector's state
	// directory and as private.
	Dir string
	// Orchestrator and Worker are what the connector was started with. Worker
	// may be partial or empty: what it leaves out comes from runner.json's
	// worker, then from Orchestrator.
	Orchestrator Spec
	Worker       Spec
	// ConfigPath is runner.json.
	ConfigPath string
	// Skill and Subagent are the basecamp-connect skill's two files.
	Skill    string
	Subagent string
	// ConnectorLog is the file the connector's output lines are kept in.
	ConnectorLog string
	// Executable is this CLI's own binary. Agents find it first on their
	// PATH as `basecamp`, so the commands the instructions name are this
	// build's.
	Executable string
	// StartLine is what the connector says of itself when it starts: the
	// profile and whether it answers Pings.
	StartLine string
	// Recordings lists a request's recording and everything above it, for
	// rules that ask what a request sits under. Nil answers with nothing.
	Recordings func(ctx context.Context, project, recordingID int64, recordingType string) []int64
	// Held reports whether the connector's hold stands. While it does, no
	// agent is started. Nil means it never does.
	Held func(ctx context.Context) (bool, error)
	// Stop ends the connector, to start again when restart is set.
	Stop   func(restart bool)
	Logger *slog.Logger

	OrchestratorTimeout time.Duration
	WorkerTimeout       time.Duration

	// Environ is the environment agents start from. Nil means os.Environ.
	Environ func() []string
	// LookPath finds an adapter's CLI. Nil means exec.LookPath.
	LookPath func(string) (string, error)
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Retry is the wait before a failed orchestrator turn is tried again,
	// by how many have failed in a row. Nil means retryDelay.
	Retry func(failures int) time.Duration
}

// Runner delivers the connector's requests to an orchestrator session and
// runs the workers it dispatches.
type Runner struct {
	opts Options
	log  *slog.Logger

	mu sync.Mutex
	st *state
	// replies are the people waiting on an [owner] message's answer, by the
	// message's inbox id.
	replies map[int64]chan sayReply
	// turns are the running worker turns, by worker id.
	turns map[string]context.CancelFunc
	// working names the workers whose turns a goroutine is running, so one
	// worker never has two, and stopping the workers whose running turn a
	// stop ended, so the goroutine knows its turn was stopped whatever the
	// worker has been told since.
	working  map[string]bool
	stopping map[string]bool
	// doing is the tool each running worker called last.
	doing map[string]string
	// recordings caches what each event's recording sits under.
	recordings map[int64][]int64

	wake  chan struct{}
	slots chan struct{}
	wg    sync.WaitGroup
	// ctx is the run's context. It is nil until Run has continued the
	// workers the last stop left, which is when ready is closed: nothing
	// starts a worker before that.
	ctx   context.Context
	ready chan struct{}
}

type sayReply struct {
	text string
	err  error
}

// New builds a Runner over the state in opts.Dir, creating the directory's
// contents as needed. Nothing is started until Run.
func New(opts Options) (*Runner, error) {
	if opts.Profile == "" || opts.Dir == "" {
		return nil, errors.New("runner: a profile and a state directory are needed")
	}
	if err := opts.Orchestrator.Validate(false); err != nil {
		return nil, fmt.Errorf("runner: orchestrator: %w", err)
	}
	if err := opts.Worker.Validate(true); err != nil {
		return nil, fmt.Errorf("runner: worker: %w", err)
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Retry == nil {
		opts.Retry = retryDelay
	}
	if opts.OrchestratorTimeout <= 0 {
		opts.OrchestratorTimeout = DefaultOrchestratorTimeout
	}
	if opts.WorkerTimeout <= 0 {
		opts.WorkerTimeout = DefaultWorkerTimeout
	}
	config, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{opts.Dir, filepath.Join(opts.Dir, "orchestrator"), filepath.Join(opts.Dir, "work"), filepath.Join(opts.Dir, "logs"), filepath.Join(opts.Dir, "instructions"), filepath.Join(opts.Dir, "bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	st, err := loadState(filepath.Join(opts.Dir, StateFile))
	if err != nil {
		return nil, err
	}
	r := &Runner{
		opts: opts, log: opts.Logger, st: st,
		replies: map[int64]chan sayReply{}, turns: map[string]context.CancelFunc{},
		working: map[string]bool{}, stopping: map[string]bool{},
		doing: map[string]string{}, recordings: map[int64][]int64{},
		wake:  make(chan struct{}, 1),
		ready: make(chan struct{}),
		slots: make(chan struct{}, cmpInt(config.MaxWorkers, DefaultMaxWorkers)),
	}
	// runner.json's orchestrator fills in a model and an effort the start
	// did not name, for the same agent; it never changes which agent that is.
	orchestrator := opts.Orchestrator
	if config.Orchestrator.Adapter == "" || config.Orchestrator.Adapter == orchestrator.Adapter {
		orchestrator = opts.Orchestrator.Over(config.Orchestrator.Over(Spec{Adapter: orchestrator.Adapter}))
	}
	// A session belongs to the agent and the directory it was started in.
	// Either changing starts a new one; a model or an effort changing does
	// not, and applies from the next turn.
	dir := filepath.Join(opts.Dir, "orchestrator")
	if st.Orchestrator.Spec.Adapter != orchestrator.Adapter || st.Orchestrator.Dir != dir {
		// The process of a turn the last run left is kept: Run ends it
		// before anything is started, whichever agent it belonged to.
		st.Orchestrator = sessionState{Dir: dir, Process: st.Orchestrator.Process}
	}
	st.Orchestrator.Spec = orchestrator
	if err := r.linkExecutable(); err != nil {
		return nil, err
	}

	r.sweep()
	if err := os.WriteFile(r.orchestratorDoc(), []byte(r.orchestratorInstructions()), 0o600); err != nil {
		return nil, err
	}
	return r, st.save(r.statePath())
}

func cmpInt(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// retryDelay is the wait before a failed orchestrator turn is tried again.
func retryDelay(failures int) time.Duration {
	return min(time.Duration(failures)*10*time.Second, 2*time.Minute)
}

func (r *Runner) statePath() string       { return filepath.Join(r.opts.Dir, StateFile) }
func (r *Runner) orchestratorDoc() string { return filepath.Join(r.opts.Dir, "orchestrator.md") }
func (r *Runner) transcript(name string) string {
	return filepath.Join(r.opts.Dir, "logs", name+".ndjson")
}

func (r *Runner) orchestratorInstructions() string {
	home, _ := os.UserHomeDir()
	return orchestratorInstructions(r.opts.Skill, instructionValues{
		Profile: r.opts.Profile, Quoted: richtext.ShellQuote(r.opts.Profile),
		OrchestratorDoc: r.orchestratorDoc(), ConnectorLog: r.opts.ConnectorLog, Home: home,
	})
}

// linkExecutable makes this build `basecamp` in a directory of the runner's
// own, which agents get first on their PATH.
func (r *Runner) linkExecutable() error {
	if r.opts.Executable == "" {
		return nil
	}
	link := filepath.Join(r.opts.Dir, "bin", "basecamp")
	if target, err := os.Readlink(link); err == nil && target == r.opts.Executable {
		return nil
	}
	_ = os.Remove(link)
	return os.Symlink(r.opts.Executable, link)
}

// keepFiles is how long a worker's transcript and its scratch directory are
// kept after they were last written.
const keepFiles = 14 * 24 * time.Hour

// maxTranscript is how large a session's transcript may grow before the next
// turn starts it over. The orchestrator's is the one that would: its session
// goes on for as long as the connector does.
const maxTranscript = 64 << 20

// sweep removes the transcripts, instructions and scratch directories of
// workers long gone from the state. One the state still holds keeps its
// own: a later request on its thread continues it.
func (r *Runner) sweep() {
	live := map[string]bool{"orchestrator": true}
	for _, w := range r.st.Workers {
		live[w.ID] = true
	}
	for _, dir := range []string{filepath.Join(r.opts.Dir, "logs"), filepath.Join(r.opts.Dir, "instructions"), filepath.Join(r.opts.Dir, "work")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil || live[strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".ndjson"), ".md")] || r.opts.Now().Sub(info.ModTime()) < keepFiles {
				continue
			}
			_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
		}
	}
}

// sessionMarkers are variables a coding agent sets for the commands it runs,
// to say they are inside one of its sessions. A connector started from such
// a session would hand them to every agent it starts, which would then read
// itself as a child of a session it has nothing to do with.
var sessionMarkers = []string{
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_CODE_EXECPATH", "CLAUDE_CODE_SSE_PORT", "CLAUDE_PID", "CLAUDE_EFFORT", "CLAUDE_PLUGIN_DATA",
	"CODEX_COMPANION_SESSION_ID", "CODEX_COMPANION_TRANSCRIPT_PATH", "CODEX_THREAD_ID",
	// The connector refuses to start with this set; an agent never gets one.
	"BASECAMP_TOKEN",
}

// Variables an agent is started with that say which runner started it, for
// the commands it runs against that runner: `basecamp connect dispatch` and
// the rest. They reach the runner's socket by them, without reading the
// profile's connect.json, whose ownership cannot be checked from inside a
// coding agent's sandbox. Knowing where the socket is grants nothing: the
// socket's own permissions decide who may use it.
const (
	SocketEnv  = "BASECAMP_CONNECT_RUNNER_SOCKET"
	ProfileEnv = "BASECAMP_CONNECT_RUNNER_PROFILE"
)

// environ is the environment an agent starts with: the connector's own, as
// a session the person opened would have, without the markers above, with
// this build first on PATH, and naming this runner.
func (r *Runner) environ() []string {
	bin := ""
	if r.opts.Executable != "" {
		bin = filepath.Join(r.opts.Dir, "bin")
	}
	base := slices.DeleteFunc(r.opts.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, SocketEnv+"=") || strings.HasPrefix(kv, ProfileEnv+"=")
	})
	// The socket's path itself, not the directory to work it out from: an
	// agent's own temporary directory, which a long path falls back to, need
	// not be the connector's.
	return append(agentEnviron(base, bin), SocketEnv+"="+SocketPath(r.opts.Dir), ProfileEnv+"="+r.opts.Profile)
}

// agentEnviron is base without the session markers, and with bin, when
// given, first on PATH.
func agentEnviron(base []string, bin string) []string {
	var out []string
	path := ""
	for _, kv := range base {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "PATH":
			path = value
		case slices.Contains(sessionMarkers, name):
		default:
			out = append(out, kv)
		}
	}
	if bin != "" {
		path = strings.TrimSuffix(bin+string(os.PathListSeparator)+path, string(os.PathListSeparator))
	}
	return append(out, "PATH="+path)
}

// Deliver takes one trusted request from the connector's handoff and keeps
// it. It returns once the request is in the state file, so the connector
// closes its record only for a request the runner still has after a crash.
// A request delivered twice is kept once.
func (r *Runner) Deliver(_ context.Context, line connector.HandoffLine) error {
	body, err := json.Marshal(line)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.st.Requests[line.EventID]; seen {
		r.log.Info("runner: a request delivered again is kept once", "event_id", line.EventID)
		return nil
	}
	req := request{RecordingID: line.Recording.RecordingID, RecordingType: line.Recording.Type, Ping: line.Ping, At: r.opts.Now(), Line: string(body)}
	if line.ReplyTo.RecordingID > 0 {
		req.Thread = fmt.Sprintf("%s:%d", line.ReplyTo.Kind, line.ReplyTo.RecordingID)
	}
	if !line.Ping {
		req.Project = line.Recording.BucketID
	}
	r.st.Requests[line.EventID] = req
	r.addItem(kindRequest, line.EventID, string(body))
	if err := r.st.save(r.statePath()); err != nil {
		delete(r.st.Requests, line.EventID)
		r.st.Inbox = r.st.Inbox[:len(r.st.Inbox)-1]
		return fmt.Errorf("runner: keep request %d: %w", line.EventID, err)
	}
	r.signal()
	return nil
}

// addItem appends a message for the orchestrator. r.mu is held.
func (r *Runner) addItem(kind string, eventID int64, body string) int64 {
	r.st.Seq++
	r.st.Inbox = append(r.st.Inbox, item{ID: r.st.Seq, Kind: kind, EventID: eventID, Body: body, At: r.opts.Now()})
	return r.st.Seq
}

func (r *Runner) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run serves the runner's socket, continues the workers a stop interrupted,
// and gives the orchestrator its messages one turn at a time, until ctx
// ends. Stopping ends every running turn; their sessions are continued at
// the next start.
func (r *Runner) Run(ctx context.Context) error {
	listener, err := listen(ctx, SocketPath(r.opts.Dir))
	if err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	defer listener.Close()
	r.wg.Go(func() { r.serve(ctx, listener) })
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	if err := r.endLeftovers(); err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	r.mu.Lock()
	r.ctx = ctx
	r.continueWorkers() //nolint:contextcheck // workers run under r.ctx, set on the line above
	config, configErr := r.config()
	if !slices.ContainsFunc(r.st.Inbox, func(it item) bool { return it.Kind == kindConnector }) {
		r.addItem(kindConnector, 0, "The connector started. "+r.opts.StartLine+" Permissions are "+string(config.Mode())+".")
	}
	err = r.st.save(r.statePath())
	orchestrator, worker := r.st.Orchestrator.Spec, r.baseWorker(config)
	r.mu.Unlock()
	if err = errors.Join(err, configErr); err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	close(r.ready)
	r.log.Info("runner: running", "orchestrator", orchestrator.String(), "worker", worker.String(), "permissions", string(config.Mode()), "state", r.opts.Dir)
	agents := []string{orchestrator.Adapter, worker.Adapter}
	for _, rule := range config.Rules {
		agents = append(agents, rule.Worker.Adapter)
	}
	for _, line := range PermissionsNotice(config.Mode(), r.opts.Profile, agents...) {
		r.log.Warn("runner: " + line)
	}

	failures := 0
	for {
		batch := r.nextBatch()
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				r.wg.Wait()
				return nil
			case <-r.wake:
			}
			continue
		}
		if err := r.orchestrate(ctx, batch); err != nil {
			if ctx.Err() != nil {
				r.wg.Wait()
				return nil
			}
			failures++
			r.log.Error("runner: the orchestrator's turn failed; it is tried again", "error", err, "failures", failures)
			select {
			case <-ctx.Done():
			case <-time.After(r.opts.Retry(failures)):
			}
			continue
		}
		failures = 0
	}
}

// endLeftovers ends every agent process the state still names. A run clears
// a turn's process when the turn ends, however it ends, so one still named
// at a start was left by a run that crashed, and may be working yet: a
// session continued beside it would do its request twice. Each is ended by
// the process group recorded for it, and only while that group is still
// provably the one recorded (driver.TerminateRecorded), and then confirmed
// gone (driver.ConfirmGroupGone): the first does not wait on its last signal
// and leaves a group whose leader has gone to the second.
//
// A group that cannot be confirmed gone keeps its record and stops the
// start, whichever session it was and whatever state its worker is in. A
// worker set aside instead could still be told more, or take a later request
// on its thread, beside the process nobody could end.
func (r *Runner) endLeftovers() error {
	r.mu.Lock()
	type leftover struct {
		worker  string // empty for the orchestrator
		process driver.Process
	}
	var left []leftover
	if p := r.st.Orchestrator.Process; p != nil {
		left = append(left, leftover{"", *p})
	}
	for _, w := range r.st.Workers {
		if p := w.Session.Process; p != nil {
			left = append(left, leftover{w.ID, *p})
		}
	}
	r.mu.Unlock()

	var blocked []error
	for _, l := range left {
		signaled, ended := driver.TerminateRecorded(l.process, turnGrace)
		name := "the orchestrator"
		if l.worker != "" {
			name = "worker " + l.worker
		}
		if err := driver.ConfirmGroupGone(l.process, turnGrace); err != nil {
			blocked = append(blocked, fmt.Errorf("the coding agent the last run left running for %s (process %d) could not be confirmed ended, and nothing is started beside it: end it, then start the connector again: %w", name, l.process.PID, err))
			continue
		}
		r.mu.Lock()
		if w := r.st.worker(l.worker); w != nil {
			w.Session.Process = nil
		} else if l.worker == "" {
			r.st.Orchestrator.Process = nil
		}
		r.mu.Unlock()
		if signaled || ended != nil {
			r.log.Warn("runner: ended an agent the last run left running when it stopped without ending it", "session", name, "pid", l.process.PID)
		}
	}
	return errors.Join(blocked...)
}

// Ready is closed once Run is serving: the socket answers and the workers
// the last stop interrupted are running again.
func (r *Runner) Ready() <-chan struct{} { return r.ready }

// nextBatch is what the orchestrator's next turn carries: the oldest
// messages, up to maxBatch. An [owner] message goes alone, so that the
// turn's answer is the answer to it.
func (r *Runner) nextBatch() []item {
	r.mu.Lock()
	defer r.mu.Unlock()
	var batch []item
	for _, it := range r.st.Inbox {
		if it.Kind == kindOwner {
			if len(batch) == 0 {
				batch = append(batch, it)
			}
			break
		}
		batch = append(batch, it)
		if len(batch) == maxBatch {
			break
		}
	}
	return batch
}

// orchestrate runs one orchestrator turn over batch. A turn that succeeds
// takes its messages out of the inbox; one that fails leaves them for the
// next, until each has failed maxAttempts times.
func (r *Runner) orchestrate(ctx context.Context, batch []item) error {
	if err := r.released(ctx); err != nil {
		return err
	}
	mode, err := r.permissions(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	session := r.st.Orchestrator
	// Before the messages are counted as tried: a turn that is refused here
	// reached no agent, and is not one of a message's attempts.
	if err := CheckPermissions(mode, session.Spec); err != nil {
		r.mu.Unlock()
		return err
	}
	adapter := adapterByName(session.Spec.Adapter)
	var parts []string
	if session.ID == "" {
		parts = append(parts, r.orchestratorInstructions())
		if digest := r.digest(); digest != "" {
			parts = append(parts, digest)
		}
	}
	for i := range r.st.Inbox {
		if slices.ContainsFunc(batch, func(it item) bool { return it.ID == r.st.Inbox[i].ID }) {
			r.st.Inbox[i].Attempts++
			parts = append(parts, formatItem(r.st.Inbox[i]))
		}
	}
	err = r.st.save(r.statePath())
	r.mu.Unlock()
	if err != nil {
		return err
	}

	turn := Turn{Role: RoleOrchestrator, Spec: session.Spec, Resume: session.ID, Dir: session.Dir, Permissions: mode}
	if session.ID == "" {
		turn.NewID = adapter.NewSessionID()
	}
	started := r.opts.Now()
	turnCtx, cancel := context.WithTimeout(ctx, r.opts.OrchestratorTimeout)
	result, err := r.turn(turnCtx, "orchestrator", adapter, turn, strings.Join(parts, "\n\n"), func(process driver.Process) {
		r.mu.Lock()
		r.st.Orchestrator.Process = &process
		_ = r.st.save(r.statePath())
		r.mu.Unlock()
	}, func(event Event) {
		if event.Kind == EventSession && event.SessionID != "" {
			r.mu.Lock()
			r.st.Orchestrator.ID = event.SessionID
			_ = r.st.save(r.statePath())
			r.mu.Unlock()
		}
	})
	timedOut := turnCtx.Err() != nil && ctx.Err() == nil
	cancel()
	// The turn's process is ended, whichever way the turn went.
	r.mu.Lock()
	r.st.Orchestrator.Process = nil
	saveErr := r.st.save(r.statePath())
	r.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if saveErr != nil {
		return saveErr
	}
	if timedOut {
		err = fmt.Errorf("%w: it ran longer than %s", ErrTurnFailed, r.opts.OrchestratorTimeout)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]int64, 0, len(batch))
	for _, it := range batch {
		ids = append(ids, it.ID)
	}
	cost := r.st.Orchestrator.charge(result)
	if err != nil {
		r.st.Orchestrator.Failures++
		if r.st.Orchestrator.Failures >= 2 && r.st.Orchestrator.ID != "" {
			// A session that keeps failing may be one its CLI can no longer
			// open. The next turn starts a new one, instructions and all.
			r.log.Warn("runner: starting a new orchestrator session after repeated failures", "session", r.st.Orchestrator.ID)
			r.st.Orchestrator.ID, r.st.Orchestrator.Turns, r.st.Orchestrator.CostUSD = "", 0, 0
		}
		r.st.Inbox = slices.DeleteFunc(r.st.Inbox, func(it item) bool {
			if !slices.Contains(ids, it.ID) || it.Attempts < maxAttempts {
				return false
			}
			r.log.Error("runner: a message the orchestrator could not handle is set aside", "kind", it.Kind, "event_id", it.EventID, "attempts", it.Attempts)
			r.st.Dropped = append(r.st.Dropped, it)
			r.answer(it.ID, sayReply{err: err})
			return true
		})
		_ = r.st.save(r.statePath())
		return err
	}
	r.st.Orchestrator.Failures = 0
	r.st.Orchestrator.Turns++
	r.st.Inbox = slices.DeleteFunc(r.st.Inbox, func(it item) bool { return slices.Contains(ids, it.ID) })
	for _, id := range ids {
		r.answer(id, sayReply{text: result.Text})
	}
	if len(result.Refused) > 0 {
		r.log.Warn("runner: the orchestrator's coding agent refused some of what it tried, by a rule of its own settings", "refused", describeRefused(result.Refused))
	}
	r.log.Info("runner: orchestrator turn", "messages", len(batch), "took", r.opts.Now().Sub(started).Round(time.Second),
		"cost_usd", roundCost(cost), "said", clip(richtext.SanitizeSingleLine(result.Text), 400))
	return r.st.save(r.statePath())
}

// released returns once the connector's hold does not stand, or ctx ends. A
// hold means nothing is dispatched or posted until it is released, and that
// is as true of what the runner already holds (its inbox, a worker waiting
// its turn) as of what the handoff has yet to hand it.
func (r *Runner) released(ctx context.Context) error {
	if r.opts.Held == nil {
		return nil
	}
	said := false
	for {
		held, err := r.opts.Held(ctx)
		if err == nil && !held {
			if said {
				r.log.Info("runner: the hold is released; agents are started again")
			}
			return nil
		}
		if !said {
			r.log.Warn("runner: the hold stands, so no agent is started until `basecamp connect release`", "error", err)
			said = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.opts.Retry(1)):
		}
	}
}

// permissions is what agents may do, as runner.json says now. A file that
// cannot be read is waited out rather than guessed around: reading it as
// "full" would start an agent with prompts off for a person who had chosen
// guarded. It returns only for a readable file, or when ctx ends.
func (r *Runner) permissions(ctx context.Context) (Permissions, error) {
	said := false
	for {
		config, err := r.config()
		if err == nil {
			if said {
				r.log.Info("runner: runner.json is readable again")
			}
			return config.Mode(), nil
		}
		if !said {
			r.log.Error("runner: no agent is started until runner.json can be read, since it says what agents may do", "error", err)
			said = true
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(r.opts.Retry(1)):
		}
	}
}

// PermissionsNotice says, in sentences a person can act on, what the agents
// a connector starts may do on this computer under mode, for the coding
// agents named, and how to change it.
func PermissionsNotice(mode Permissions, profile string, agents ...string) []string {
	if mode != PermissionsGuarded {
		return []string{"your agent's coding agents can run any command and change any file on this computer, as you, without asking: nobody is at the keyboard to approve each step. To hold them back, start with --permissions guarded. Who may give your agent work is yours to set: basecamp connect show -P " + richtext.ShellQuote(profile)}
	}
	lines := []string{"your agent's coding agents run with guarded permissions: each under its own guardrails, with no one asked. What that means differs by coding agent, and an agent that is stopped says so in its reply. To lift it, start with --permissions full."}
	if slices.Contains(agents, "claude") {
		lines = append(lines, "guarded, Claude Code: it runs in its auto mode, judging each action itself and stopping the ones it finds unsafe.")
	}
	if slices.Contains(agents, "codex") {
		lines = append(lines, "guarded, Codex: it runs each command in its sandbox, where it can write only in the folder it works in and the temporary directory, and decides itself, with no one asked, whether a command the sandbox stopped may run outside it.")
	}
	return lines
}

// describeRefused is the refused tools as a person reads them: "Bash ×2, Edit".
func describeRefused(tools []string) string {
	counts := map[string]int{}
	var order []string
	for _, tool := range tools {
		if counts[tool] == 0 {
			order = append(order, tool)
		}
		counts[tool]++
	}
	parts := make([]string, 0, len(order))
	for _, tool := range order {
		if counts[tool] > 1 {
			parts = append(parts, fmt.Sprintf("%s ×%d", tool, counts[tool]))
		} else {
			parts = append(parts, tool)
		}
	}
	return strings.Join(parts, ", ")
}

// answer gives a waiting `say` its reply. r.mu is held.
func (r *Runner) answer(itemID int64, reply sayReply) {
	if ch, ok := r.replies[itemID]; ok {
		ch <- reply
		delete(r.replies, itemID)
	}
}

// digest tells a new orchestrator session what an earlier one left in
// flight. r.mu is held.
func (r *Runner) digest() string {
	var lines []string
	for _, w := range r.st.Workers {
		if w.live() {
			lines = append(lines, fmt.Sprintf("- %s", describeWorker(w, w.EventID)))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "[connector] This is a new session of yours; an earlier one dispatched these workers, which are still at work and will report to you:\n" + strings.Join(lines, "\n")
}

// formatItem is a message as the orchestrator reads it.
func formatItem(it item) string {
	again := ""
	if it.Attempts > 1 {
		again = " (delivered again: the turn that first carried this did not finish. Check `basecamp connect workers list` and the thread before repeating anything.)"
	}
	switch it.Kind {
	case kindRequest:
		return "[basecamp-request]" + again + "\n" + it.Body
	case kindOwner:
		return "[owner]" + again + "\n" + it.Body
	case kindReport:
		return "[worker-report]" + again + " " + it.Body
	default:
		return "[connector]" + again + " " + it.Body
	}
}

// turn runs one turn of a session, keeping its raw stream in the session's
// transcript.
func (r *Runner) turn(ctx context.Context, name string, adapter Adapter, turn Turn, prompt string, started func(driver.Process), events func(Event)) (TurnResult, error) {
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if info, err := os.Stat(r.transcript(name)); err == nil && info.Size() > maxTranscript {
		flags |= os.O_TRUNC
	}
	transcript, err := os.OpenFile(r.transcript(name), flags, 0o600)
	if err != nil {
		return TurnResult{}, err
	}
	defer transcript.Close()
	header, _ := json.Marshal(map[string]any{"type": "runner.turn", "at": r.opts.Now().UTC(), "adapter": adapter.Name(), "resume": turn.Resume, "prompt_bytes": len(prompt)})
	_, _ = transcript.Write(append(header, '\n'))
	return runTurn(ctx, turnRequest{
		Adapter: adapter, Turn: turn, Prompt: prompt, Env: r.environ(),
		Transcript: transcript, Events: events, LookPath: r.opts.LookPath, Started: started,
	})
}

// roundCost is a cost to the tenth of a cent, which is as exact as a log
// line needs it.
func roundCost(usd float64) float64 { return math.Round(usd*1000) / 1000 }

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
