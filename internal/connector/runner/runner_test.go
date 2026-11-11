package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// harness is a Runner over stand-in agents, and what they were started with.
type harness struct {
	t      *testing.T
	opts   Options
	runner *Runner
	log    string
	cancel context.CancelFunc
	done   chan error
}

// newHarness builds a runner in a fresh directory and runs it. change edits
// its options before it is built.
func newHarness(t *testing.T, change func(*Options)) *harness {
	t.Helper()
	h := unstartedHarness(t, change)
	h.start()
	return h
}

// unstartedHarness is a harness whose runner is neither built nor run, for a
// test about a start that fails.
func unstartedHarness(t *testing.T, change func(*Options)) *harness {
	t.Helper()
	look, log := fakeAgents(t)
	// Short, so the socket's path fits whatever the test's own name is.
	dir, err := os.MkdirTemp("", "runner")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	opts := Options{
		Profile: "agent", Dir: dir,
		Orchestrator: Spec{Adapter: "claude", Model: "opus"},
		ConfigPath:   filepath.Join(dir, ConfigFile),
		Skill:        "---\nname: basecamp-connect\n---\n\nSKILL BODY\n", Subagent: "SUBAGENT BODY\n",
		ConnectorLog: filepath.Join(dir, "connector.ndjson"), Executable: "/opt/basecamp/bin/basecamp",
		StartLine: "It runs as profile agent.",
		LookPath:  look,
		Environ: func() []string {
			return []string{"PATH=/usr/bin", "HOME=" + dir, "CLAUDECODE=1", "BASECAMP_TOKEN=secret", fakeLogEnv + "=" + log, fakeDirEnv + "=" + dir}
		},
		Retry: func(int) time.Duration { return 10 * time.Millisecond },
	}
	if change != nil {
		change(&opts)
	}
	return &harness{t: t, opts: opts, log: log}
}

// start builds the runner over the harness's directory and runs it.
func (h *harness) start() {
	h.t.Helper()
	r, err := New(h.opts)
	require.NoError(h.t, err)
	ctx, cancel := context.WithCancel(context.Background())
	h.runner, h.cancel, h.done = r, cancel, make(chan error, 1)
	go func() { h.done <- r.Run(ctx) }()
	h.t.Cleanup(h.stop)
	select {
	case <-r.Ready():
	case <-time.After(10 * time.Second):
		h.t.Fatal("the runner did not start")
	}
}

// stop ends the run and waits for it, as a signal to the connector would.
func (h *harness) stop() {
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err
	case <-time.After(20 * time.Second):
		h.t.Error("the runner did not stop")
	}
}

func (h *harness) request(eventID int64, content string) connector.HandoffLine {
	return connector.HandoffLine{
		Type: "request", EventID: eventID, Trigger: "mentioned", Content: content, Role: "operator",
		Recording: connector.HandoffRecording{BucketID: 456, RecordingID: 789, Type: "Comment"},
	}
}

// settled waits until the orchestrator has nothing left to handle and no
// worker is live.
func (h *harness) settled() {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		status := h.runner.Status()
		return status.Waiting == 0 && status.Running == 0 && status.Queued == 0
	}, 20*time.Second, 20*time.Millisecond)
}

func (h *harness) invocations() []fakeInvocation { return invocations(h.t, h.log) }

// by is the invocations of one agent.
func by(all []fakeInvocation, agent string) []fakeInvocation {
	return slices.DeleteFunc(slices.Clone(all), func(inv fakeInvocation) bool { return inv.Agent != agent })
}

func TestARequestIsHandedToTheOrchestratorWhichDispatchesAWorkerThatReportsBack(t *testing.T) {
	repo := t.TempDir()
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "codex", Effort: "high"} })
	require.NoError(t, h.runner.Deliver(context.Background(), h.request(101, "REPO="+repo+" fix it")))
	h.settled()

	orchestrator, workers := by(h.invocations(), "claude"), by(h.invocations(), "codex")
	require.Len(t, workers, 1)
	require.GreaterOrEqual(t, len(orchestrator), 2)

	// The orchestrator's session starts with the mechanics and the skill,
	// without the skill's frontmatter, and is continued by its id after.
	first := orchestrator[0]
	assert.Contains(t, first.Prompt, "You are the orchestrator of a Basecamp agent")
	assert.Contains(t, first.Prompt, "SKILL BODY")
	assert.NotContains(t, first.Prompt, "name: basecamp-connect")
	assert.Contains(t, first.Prompt, "basecamp connect dispatch -P agent --event <event_id>")
	assert.Contains(t, first.Args, "--session-id")
	assert.Equal(t, []string{"--model", "opus"}, first.Args[slices.Index(first.Args, "--model"):slices.Index(first.Args, "--model")+2])
	assert.Equal(t, filepath.Join(h.opts.Dir, "orchestrator"), first.Dir)
	session := first.Args[slices.Index(first.Args, "--session-id")+1]
	for _, later := range orchestrator[1:] {
		assert.Equal(t, session, later.Args[slices.Index(later.Args, "--resume")+1])
		assert.NotContains(t, later.Prompt, "SKILL BODY", "the instructions are given once per session")
	}

	// Somewhere in its turns: the start, the request line, and the report.
	said := ""
	for _, inv := range orchestrator {
		said += inv.Prompt + "\n"
	}
	assert.Contains(t, said, "[connector] The connector started. It runs as profile agent.")
	assert.Contains(t, said, `[basecamp-request]`)
	assert.Contains(t, said, `"event_id":101`)
	assert.Contains(t, said, "[worker-report] worker w")
	assert.Contains(t, said, "finished. Its report:\ndone")

	// The worker ran on the agent the start named, in the repo, with the
	// worker instructions and the handoff, and the prompt never in argv.
	worker := workers[0]
	assert.Equal(t, repo, worker.Dir)
	assert.Equal(t, []string{"exec", "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-c", `model_reasoning_effort="high"`, "-"}, worker.Args)
	assert.Contains(t, worker.Prompt, "You are a worker of a Basecamp agent")
	assert.Contains(t, worker.Prompt, "SUBAGENT BODY")
	assert.Contains(t, worker.Prompt, "handoff for event 101")
	assert.Contains(t, worker.Prompt, "You already start in the repo the orchestrator chose, `"+repo+"`")

	// Agents get this build first on PATH and none of the markers of the
	// session the connector may have been started from.
	assert.Equal(t, filepath.Join(h.opts.Dir, "bin")+":/usr/bin", worker.Path)
	assert.Empty(t, worker.Marker)
	assert.Equal(t, SocketPath(h.opts.Dir)+" agent", worker.Runner, "an agent is told which runner started it, for the commands it runs against it")
	target, err := os.Readlink(filepath.Join(h.opts.Dir, "bin", "basecamp"))
	require.NoError(t, err)
	assert.Equal(t, "/opt/basecamp/bin/basecamp", target)

	views := h.runner.Workers()
	require.Len(t, views, 1)
	assert.Equal(t, workerDone, views[0].State)
	assert.Equal(t, "codex", views[0].Adapter)
	assert.Equal(t, "high", views[0].Effort)
	assert.Equal(t, "done", views[0].Report)
}

func TestARequestDeliveredTwiceIsHandledOnce(t *testing.T) {
	h := newHarness(t, nil)
	line := h.request(7, "hello")
	require.NoError(t, h.runner.Deliver(context.Background(), line))
	require.NoError(t, h.runner.Deliver(context.Background(), line))
	h.settled()
	assert.Len(t, h.runner.Workers(), 1)
	seen := 0
	for _, inv := range h.invocations() {
		seen += strings.Count(inv.Prompt, "[basecamp-request]\n{")
	}
	assert.Equal(t, 1, seen)

	// And a second dispatch for its event returns the worker it has.
	view, existing, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 7, NoRepo: true, Handoff: "again"})
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, h.runner.Workers()[0].ID, view.ID)
	_, existing, err = h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 7, NoRepo: true, Handoff: "again", Again: true})
	require.NoError(t, err)
	assert.False(t, existing)
	h.settled()
	assert.Len(t, h.runner.Workers(), 2)
}

func TestRulesChooseTheWorkerByProjectRepoAndWhatTheRequestSitsUnder(t *testing.T) {
	repo := t.TempDir()
	var asked []int64
	h := newHarness(t, func(o *Options) {
		o.Worker = Spec{Adapter: "codex", Model: "gpt", Effort: "low"}
		o.Recordings = func(_ context.Context, project, recordingID int64, recordingType string) []int64 {
			asked = append(asked, project, recordingID)
			assert.Equal(t, "Comment", recordingType)
			return []int64{recordingID, 900, 5000}
		}
	})
	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{
		"rules": [
			{"repo": "`+repo+`", "worker": {"adapter": "claude", "model": "sonnet"}},
			{"card_table": 5000, "worker": {"effort": "high"}},
			{"ping": true, "worker": {"adapter": "pi"}}
		]}`), 0o600))
	h.settled()

	// The file is read for each dispatch: it was written after the start.
	inRepo := h.request(1, "x")
	onTable := h.request(2, "x")
	ping := h.request(3, "x")
	ping.Ping = true
	for _, line := range []connector.HandoffLine{inRepo, onTable, ping} {
		h.runner.mu.Lock()
		h.runner.st.Requests[line.EventID] = request{Project: 456, RecordingID: 789, RecordingType: "Comment", Ping: line.Ping}
		h.runner.mu.Unlock()
	}
	first, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 1, Repo: repo, Handoff: "x"})
	require.NoError(t, err)
	assert.Equal(t, Spec{Adapter: "claude", Model: "sonnet", Effort: "low"}, Spec{Adapter: first.Adapter, Model: first.Model, Effort: first.Effort},
		"a change of agent drops the default's model and keeps its effort")
	assert.Equal(t, "rule 1 (repo "+repo+")", first.Matched)

	second, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 2, NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	assert.Equal(t, Spec{Adapter: "codex", Model: "gpt", Effort: "high"}, Spec{Adapter: second.Adapter, Model: second.Model, Effort: second.Effort})
	assert.Equal(t, "rule 2 (card table 5000)", second.Matched)

	third, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 3, NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	assert.Equal(t, "pi", third.Adapter)
	assert.Empty(t, third.Model)

	// What the dispatch itself names wins over every rule.
	fourth, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 1, Repo: repo, Handoff: "x", Again: true, Spec: Spec{Adapter: "pi", Model: "p/m"}})
	require.NoError(t, err)
	assert.Equal(t, Spec{Adapter: "pi", Model: "p/m", Effort: "low"}, Spec{Adapter: fourth.Adapter, Model: fourth.Model, Effort: fourth.Effort})
	h.settled()
	assert.Equal(t, []int64{456, 789, 456, 789}, asked[:4], "what a request sits under is read for a request in a project, and not for a Ping")

	pis := by(h.invocations(), "pi")
	require.Len(t, pis, 2)
	named := pis[slices.IndexFunc(pis, func(inv fakeInvocation) bool { return slices.Contains(inv.Args, "--model") })]
	assert.Equal(t, []string{"--model", "p/m", "--thinking", "low"}, named.Args[len(named.Args)-4:])

	// A rules file that cannot be read stops the dispatch and says why.
	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{"rules":[{"projekt":1}]}`), 0o600))
	_, _, err = h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "x"})
	require.ErrorContains(t, err, "runner.json")
}

func TestDispatchRefusesWhatItCannotRun(t *testing.T) {
	h := newHarness(t, nil)
	for name, req := range map[string]DispatchRequest{
		"is not a request this connector handed off": {EventID: 99, NoRepo: true, Handoff: "x"},
		"say where the work goes":                    {Handoff: "x"},
		"is not a directory":                         {Repo: "/no/such/dir", Handoff: "x"},
		"not an absolute path":                       {Repo: "relative/dir", Handoff: "x"},
		"the handoff is empty":                       {NoRepo: true},
		"unknown adapter":                            {NoRepo: true, Handoff: "x", Spec: Spec{Adapter: "gemini"}},
		"is not a model name":                        {NoRepo: true, Handoff: "x", Spec: Spec{Model: "--dangerous"}},
	} {
		_, _, err := h.runner.Dispatch(context.Background(), req)
		assert.ErrorContains(t, err, name)
	}
	assert.Empty(t, h.runner.Workers())
}

func TestAWorkerInterruptedByAStopIsContinuedInItsSessionAtTheNextStart(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "pi"} })
	require.NoError(t, h.runner.Deliver(context.Background(), h.request(55, "SLOW-WORKER")))
	require.Eventually(t, func() bool {
		workers := h.runner.Workers()
		return len(workers) == 1 && workers[0].State == workerRunning && h.runner.Status().Waiting == 0
	}, 20*time.Second, 20*time.Millisecond)
	// The session is the worker's once its CLI has said so.
	require.Eventually(t, func() bool {
		h.runner.mu.Lock()
		defer h.runner.mu.Unlock()
		return h.runner.st.Workers[0].Session.ID != ""
	}, 10*time.Second, 20*time.Millisecond)
	// While its turn runs, the state names the CLI's process, which is what
	// a start after a crash ends. A stop ends it itself and names none.
	saved, err := loadState(filepath.Join(h.opts.Dir, StateFile))
	require.NoError(t, err)
	require.NotNil(t, saved.Workers[0].Session.Process)
	assert.Positive(t, saved.Workers[0].Session.Process.PID)
	h.stop()
	require.NoError(t, <-h.done)
	saved, err = loadState(filepath.Join(h.opts.Dir, StateFile))
	require.NoError(t, err)
	assert.Nil(t, saved.Workers[0].Session.Process)
	assert.Equal(t, workerRunning, saved.Workers[0].State)

	h.start()
	h.settled()
	workers := by(h.invocations(), "pi")
	require.Len(t, workers, 2)
	assert.Equal(t, workers[0].Args[slices.Index(workers[0].Args, "--session-id")+1], workers[1].Args[slices.Index(workers[1].Args, "--session")+1])
	assert.True(t, strings.HasPrefix(workers[1].Prompt, "[connector] The connector stopped while you were working"))
	assert.NotContains(t, workers[1].Prompt, "HANG")
	assert.Equal(t, workerDone, h.runner.Workers()[0].State)

	// The orchestrator, in the same session as before, hears the report.
	orchestrator := by(h.invocations(), "claude")
	last := orchestrator[len(orchestrator)-1]
	assert.Contains(t, last.Args, "--resume")
	assert.Contains(t, last.Prompt, "[worker-report] worker")
}

func TestAFailedOrchestratorTurnIsTriedAgainWithItsMessages(t *testing.T) {
	h := newHarness(t, nil)
	h.settled()
	reply, err := h.runner.Say(context.Background(), "FAIL-ONCE what is running?")
	require.NoError(t, err)
	assert.Equal(t, "done", reply)
	var attempts []fakeInvocation
	for _, inv := range by(h.invocations(), "claude") {
		if strings.Contains(inv.Prompt, "FAIL-ONCE") {
			attempts = append(attempts, inv)
		}
	}
	require.Len(t, attempts, 2)
	assert.Contains(t, attempts[0].Prompt, "[owner]\nFAIL-ONCE")
	assert.Contains(t, attempts[1].Prompt, "[owner] (delivered again")
}

func TestAMessageTheOrchestratorKeepsFailingOnIsSetAsideAndTheNextIsHandled(t *testing.T) {
	h := newHarness(t, nil)
	h.settled()
	_, err := h.runner.Say(context.Background(), "FAIL-ALWAYS")
	require.ErrorContains(t, err, "scripted failure")
	status := h.runner.Status()
	assert.Equal(t, 1, status.SetAside)
	assert.Equal(t, 0, status.Waiting)

	// Repeated failures start a new session, which gets the instructions again.
	reply, err := h.runner.Say(context.Background(), "still there?")
	require.NoError(t, err)
	assert.Equal(t, "done", reply)
	all := by(h.invocations(), "claude")
	last := all[len(all)-1]
	assert.Contains(t, last.Args, "--session-id")
	assert.Contains(t, last.Prompt, "SKILL BODY")
}

func TestAWorkerThatCannotRunIsReportedToTheOrchestrator(t *testing.T) {
	h := newHarness(t, nil)
	h.settled()
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "FAIL-ALWAYS"})
	require.NoError(t, err)
	h.settled()
	workers := h.runner.Workers()
	require.Len(t, workers, 1)
	assert.Equal(t, workerFailed, workers[0].State)
	assert.Contains(t, workers[0].Error, "scripted failure")
	// Tried once more, in the session its CLI had opened, before giving up.
	turns := 0
	for _, inv := range by(h.invocations(), "claude") {
		if strings.Contains(inv.Prompt, "FAIL-ALWAYS") || strings.Contains(inv.Prompt, "Your last turn ended in an error") {
			turns++
		}
	}
	assert.Equal(t, 2, turns)
	all := by(h.invocations(), "claude")
	assert.Contains(t, all[len(all)-1].Prompt, "[worker-report] worker "+view.ID+" (claude · opus, no repo) could not run: the turn failed: scripted failure. Nobody has replied to its request.")
}

func TestSendContinuesAFinishedWorkerAndStopEndsARunningOne(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "codex"} })
	h.settled()
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "first"})
	require.NoError(t, err)
	h.settled()
	_, err = h.runner.Send(view.ID, "one more thing")
	require.NoError(t, err)
	h.settled()
	turns := by(h.invocations(), "codex")
	require.Len(t, turns, 2)
	assert.Equal(t, []string{"exec", "resume"}, turns[1].Args[:2])
	assert.Regexp(t, `^codex-thread-\d+$`, turns[1].Args[2])
	assert.Equal(t, []string{"--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-"}, turns[1].Args[3:])
	assert.Equal(t, "[orchestrator] one more thing", turns[1].Prompt)
	assert.Equal(t, filepath.Join(h.opts.Dir, "work", view.ID), turns[1].Dir, "a session is continued from the directory it started in")

	hung, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "HANG"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return h.runner.Status().Running == 1 }, 10*time.Second, 20*time.Millisecond)
	stopped, err := h.runner.StopWorker(hung.ID)
	require.NoError(t, err)
	assert.Equal(t, workerStopped, stopped.State)
	h.settled()
	_, err = h.runner.Send("w999", "x")
	require.ErrorContains(t, err, "there is no worker")
}

func TestTheSocketServesTheCommandsAnAgentRuns(t *testing.T) {
	stopped := make(chan bool, 1)
	h := newHarness(t, func(o *Options) { o.Stop = func(restart bool) { stopped <- restart } })
	h.settled()
	ctx := context.Background()

	resp, err := Call(ctx, SocketPath(h.opts.Dir), Request{Op: OpDispatch, Dispatch: DispatchRequest{NoRepo: true, Handoff: "over the socket"}}, 10*time.Second)
	require.NoError(t, err)
	require.NotNil(t, resp.Worker)
	h.settled()

	resp, err = Call(ctx, SocketPath(h.opts.Dir), Request{Op: OpWorkers}, 10*time.Second)
	require.NoError(t, err)
	require.Len(t, resp.Workers, 1)
	assert.Equal(t, workerDone, resp.Workers[0].State)
	assert.Equal(t, Spec{Adapter: "claude", Model: "opus"}, resp.Status.Orchestrator)
	assert.Equal(t, Spec{Adapter: "claude", Model: "opus"}, resp.Status.Worker, "with no worker named, workers run on the orchestrator's agent")

	resp, err = Call(ctx, SocketPath(h.opts.Dir), Request{Op: OpSay, Message: "hello"}, 20*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "done", resp.Reply)

	_, err = Call(ctx, SocketPath(h.opts.Dir), Request{Op: OpSend, Worker: "nope", Message: "x"}, 10*time.Second)
	require.ErrorContains(t, err, "there is no worker")

	_, err = Call(ctx, SocketPath(h.opts.Dir), Request{Op: OpStop, Restart: true}, 10*time.Second)
	require.NoError(t, err)
	assert.True(t, <-stopped)

	elsewhere := t.TempDir()
	require.NoError(t, os.Chmod(elsewhere, 0o700))
	_, err = Call(ctx, SocketPath(elsewhere), Request{Op: OpWorkers}, time.Second)
	require.ErrorIs(t, err, ErrNotRunning)
	_, err = Call(ctx, filepath.Join(elsewhere, "gone", "sock"), Request{Op: OpWorkers}, time.Second)
	require.ErrorIs(t, err, ErrNotRunning)

	info, err := os.Stat(SocketPath(h.opts.Dir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTheStateFileIsPrivateAndHoldsNoMoreThanItsBounds(t *testing.T) {
	h := newHarness(t, nil)
	h.settled()
	info, err := os.Stat(filepath.Join(h.opts.Dir, StateFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	s := &state{Version: stateVersion, Requests: map[int64]request{}}
	for i := range int64(keepRequests + 20) {
		s.Requests[i+1] = request{}
	}
	for i := range keepWorkers + 5 {
		s.Workers = append(s.Workers, &workerState{ID: "w" + string(rune('a'+i%26)), State: workerDone})
	}
	s.Workers[0].State = workerRunning
	s.prune()
	assert.Len(t, s.Requests, keepRequests)
	assert.NotContains(t, s.Requests, int64(20))
	assert.Contains(t, s.Requests, int64(21))
	assert.Len(t, s.Workers, keepWorkers)
	assert.Equal(t, workerRunning, s.Workers[0].State, "a live worker is never dropped")

	data, err := os.ReadFile(filepath.Join(h.opts.Dir, StateFile))
	require.NoError(t, err)
	var saved state
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.Equal(t, "claude", saved.Orchestrator.Spec.Adapter)
	assert.NotEmpty(t, saved.Orchestrator.ID)
}

func TestAWorkerThatRunsPastItsTimeIsStoppedAndReported(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.WorkerTimeout = 300 * time.Millisecond })
	h.settled()
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "HANG"})
	require.NoError(t, err)
	h.settled()
	workers := h.runner.Workers()
	require.Len(t, workers, 1)
	assert.Equal(t, workerFailed, workers[0].State)
	assert.Equal(t, "it ran longer than 300ms and was stopped", workers[0].Error)
	all := by(h.invocations(), "claude")
	assert.Contains(t, all[len(all)-1].Prompt, "[worker-report] worker "+view.ID+" (claude · opus, no repo) could not run: it ran longer than 300ms and was stopped.")
}

func TestOnlySoManyWorkersRunAtOnce(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		require.NoError(t, os.WriteFile(o.ConfigPath, []byte(`{"max_workers": 1}`), 0o600))
	})
	h.settled()
	first, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "HANG"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return h.runner.Status().Running == 1 }, 10*time.Second, 20*time.Millisecond)
	_, _, err = h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "quick"})
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)
	status := h.runner.Status()
	assert.Equal(t, 1, status.Running)
	assert.Equal(t, 1, status.Queued)
	_, err = h.runner.StopWorker(first.ID)
	require.NoError(t, err)
	h.settled()
	assert.Equal(t, workerDone, h.runner.Workers()[1].State)
}

func TestClaudeCodesRunningTotalIsNotCountedTwice(t *testing.T) {
	var session sessionState
	assert.InDelta(t, 0.17, session.charge(TurnResult{SessionCostUSD: 0.17}), 1e-9)
	assert.InDelta(t, 0.05, session.charge(TurnResult{SessionCostUSD: 0.22}), 1e-9)
	assert.InDelta(t, 0.22, session.CostUSD, 1e-9)
	// A CLI that reports each turn's cost has them added up.
	var perTurn sessionState
	perTurn.charge(TurnResult{Usage: Usage{CostUSD: 0.1}})
	assert.InDelta(t, 0.2, perTurn.charge(TurnResult{Usage: Usage{CostUSD: 0.2}}), 1e-9)
	assert.InDelta(t, 0.3, perTurn.CostUSD, 1e-9)
}

func TestWhatWorkersLeftBehindIsSweptOnceTheStateNoLongerHoldsThem(t *testing.T) {
	h := newHarness(t, nil)
	h.settled()
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	h.settled()
	h.stop()
	require.NoError(t, <-h.done)
	transcript := filepath.Join(h.opts.Dir, "logs", view.ID+".ndjson")
	instructions := filepath.Join(h.opts.Dir, "instructions", view.ID+".md")
	scratch := filepath.Join(h.opts.Dir, "work", view.ID)

	// However old, they stay while the state holds the worker: a later
	// request on its thread continues it.
	h.opts.Now = func() time.Time { return time.Now().Add(keepFiles + time.Hour) }
	h.start()
	require.FileExists(t, transcript)
	require.FileExists(t, instructions)
	require.DirExists(t, scratch)
	h.stop()
	require.NoError(t, <-h.done)

	saved, err := loadState(filepath.Join(h.opts.Dir, StateFile))
	require.NoError(t, err)
	saved.Workers = nil
	require.NoError(t, saved.save(filepath.Join(h.opts.Dir, StateFile)))
	h.start()
	assert.NoFileExists(t, transcript)
	assert.NoFileExists(t, instructions)
	assert.NoDirExists(t, scratch)
	assert.FileExists(t, filepath.Join(h.opts.Dir, "logs", "orchestrator.ndjson"), "the orchestrator's own transcript stays")
}

// A run that crashed ended nothing: the agent it had started may still be
// working. The next start ends that process before it continues the worker,
// so the request is not worked on twice at once.
func TestAnAgentACrashLeftRunningIsEndedBeforeItsWorkerIsContinued(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep command to stand in for an agent left running")
	}
	const session = "22222222-2222-4222-8222-222222222222"
	var leftover, orchestrating *driver.Worker
	h := newHarness(t, func(o *Options) {
		leftover, err = driver.StartWorker(context.Background(), driver.SessionConfig{Scope: driver.Scope{WorkDir: o.Dir}},
			driver.Command{Path: sleep, Args: []string{"600"}})
		require.NoError(t, err)
		process := leftover.Process()
		// The orchestrator's turn too, under the coding agent the crashed
		// run was started with, which this start does not name.
		orchestrating, err = driver.StartWorker(context.Background(), driver.SessionConfig{Scope: driver.Scope{WorkDir: o.Dir}},
			driver.Command{Path: sleep, Args: []string{"600"}})
		require.NoError(t, err)
		orchestratorProcess := orchestrating.Process()
		crashed := &state{
			Version: stateVersion, WorkerSeq: 1, Requests: map[int64]request{},
			Orchestrator: sessionState{Spec: Spec{Adapter: "pi"}, ID: "old", Dir: filepath.Join(o.Dir, "orchestrator"), Process: &orchestratorProcess},
			Workers: []*workerState{{
				ID: "w1", State: workerRunning, Prompts: []prompt{{Text: "the handoff", Fresh: "the handoff"}},
				Session: sessionState{Spec: Spec{Adapter: "codex"}, ID: session, Dir: o.Dir, Process: &process},
			}},
		}
		require.NoError(t, crashed.save(filepath.Join(o.Dir, StateFile)))
	})
	t.Cleanup(func() {
		leftover.Terminate(time.Second)
		orchestrating.Terminate(time.Second)
	})

	for _, left := range []*driver.Worker{leftover, orchestrating} {
		select {
		case <-left.Done():
		case <-time.After(15 * time.Second):
			t.Fatal("a process the crashed run left is still running")
		}
	}
	h.settled()
	turns := by(h.invocations(), "codex")
	require.Len(t, turns, 1)
	assert.Equal(t, []string{"exec", "resume", session}, turns[0].Args[:3])
	assert.True(t, strings.HasPrefix(turns[0].Prompt, "[connector] The connector stopped while you were working"))
	h.runner.mu.Lock()
	defer h.runner.mu.Unlock()
	assert.Nil(t, h.runner.st.Workers[0].Session.Process, "a turn that ended names no process")
	assert.Nil(t, h.runner.st.Orchestrator.Process)
}

func TestWhatAnAgentsSettingsRefusedIsCountedAndToldToTheOrchestrator(t *testing.T) {
	h := newHarness(t, nil)
	h.settled()
	require.NoError(t, h.runner.Deliver(context.Background(), h.request(31, "plain")))
	h.settled()
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 31, NoRepo: true, Handoff: "REFUSE-ME", Again: true})
	require.NoError(t, err)
	h.settled()
	workers := h.runner.Workers()
	refused := workers[slices.IndexFunc(workers, func(w WorkerView) bool { return w.ID == view.ID })]
	assert.Equal(t, 2, refused.Refused)
	all := by(h.invocations(), "claude")
	assert.Contains(t, all[len(all)-1].Prompt, "Its coding agent refused 2 of the commands it tried")
	assert.Equal(t, "Bash ×2, Edit", describeRefused([]string{"Bash", "Edit", "Bash"}))

	// The worker is given the request line by the connector, whole, ahead
	// of what the orchestrator wrote for it.
	var handed fakeInvocation
	for _, inv := range all {
		if strings.Contains(inv.Prompt, "REFUSE-ME") {
			handed = inv
		}
	}
	assert.Contains(t, handed.Prompt, "The request line, as the connector handed it off:\n{\"type\":\"request\",\"event_id\":31,")
	assert.Less(t, strings.Index(handed.Prompt, `"event_id":31`), strings.Index(handed.Prompt, "REFUSE-ME"))
}

// onThread is a request made on a thread: a comment on recording 700.
func (h *harness) onThread(eventID int64, content string) connector.HandoffLine {
	line := h.request(eventID, content)
	line.ReplyTo = connector.InstructionReply{Kind: "comment", RecordingID: 700}
	return line
}

// A second request on a thread goes to the worker that handled the first,
// in the session it handled it in, and each request is reported on its own.
func TestALaterRequestOnTheSameThreadContinuesTheWorkerThatHandledTheFirst(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "codex"} })
	require.NoError(t, h.runner.Deliver(context.Background(), h.onThread(1, "first")))
	h.settled()
	require.NoError(t, h.runner.Deliver(context.Background(), h.onThread(2, "and another thing")))
	h.settled()

	workers := h.runner.Workers()
	require.Len(t, workers, 1, "one worker for the thread")
	assert.Equal(t, 2, workers[0].Requests)
	assert.Equal(t, int64(2), workers[0].EventID)
	assert.Equal(t, workerDone, workers[0].State)

	turns := by(h.invocations(), "codex")
	require.Len(t, turns, 2)
	assert.Equal(t, []string{"exec", "resume"}, turns[1].Args[:2], "the second request is a turn of the first one's session")
	assert.True(t, strings.HasPrefix(turns[1].Prompt, "[connector] A further request has been made on the thread you worked on before."))
	assert.Contains(t, turns[1].Prompt, `"event_id":2`)
	assert.Contains(t, turns[1].Prompt, "handoff for event 2")
	assert.NotContains(t, turns[1].Prompt, "SUBAGENT BODY", "the session already has its instructions")
	doc := filepath.Join(h.opts.Dir, "instructions", workers[0].ID+".md")
	assert.Contains(t, turns[1].Prompt, doc, "and is told where they are kept")
	kept, err := os.ReadFile(doc)
	require.NoError(t, err)
	assert.Contains(t, string(kept), "SUBAGENT BODY")

	said := ""
	for _, inv := range by(h.invocations(), "claude") {
		said += inv.Prompt + "\n"
	}
	assert.Contains(t, said, "[worker-report] worker "+workers[0].ID+" for event 1 (codex, no repo) finished.")
	assert.Contains(t, said, "[worker-report] worker "+workers[0].ID+" for event 2 (codex, no repo) finished.")

	// A request already given to it is not given again.
	view, existing, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 1, NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	assert.True(t, existing)
	assert.Equal(t, workers[0].ID, view.ID)
}

// A new worker starts when the earlier one's session could not hold the
// request: its work was in another repo or on another coding agent, the
// dispatch asked for a new one, or the thread is not the same.
func TestALaterRequestStartsANewWorkerWhenTheEarlierSessionCannotTakeIt(t *testing.T) {
	repo := t.TempDir()
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "codex"} })
	h.settled()
	for id := int64(1); id <= 5; id++ {
		line := h.onThread(id, "x")
		if id == 5 {
			line.ReplyTo.RecordingID = 701
		}
		h.runner.mu.Lock()
		h.runner.st.Requests[id] = request{Project: 456, RecordingID: 789, Thread: fmt.Sprintf("%s:%d", line.ReplyTo.Kind, line.ReplyTo.RecordingID), Line: "{}"}
		h.runner.mu.Unlock()
	}
	dispatch := func(req DispatchRequest) WorkerView {
		t.Helper()
		req.Handoff = "x"
		view, existing, err := h.runner.Dispatch(context.Background(), req)
		require.NoError(t, err)
		require.False(t, existing)
		h.settled()
		return view
	}
	first := dispatch(DispatchRequest{EventID: 1, NoRepo: true})
	assert.NotEqual(t, first.ID, dispatch(DispatchRequest{EventID: 2, Repo: repo}).ID, "another repo")
	assert.NotEqual(t, first.ID, dispatch(DispatchRequest{EventID: 3, NoRepo: true, Spec: Spec{Adapter: "pi"}}).ID, "another coding agent")
	assert.NotEqual(t, first.ID, dispatch(DispatchRequest{EventID: 5, NoRepo: true}).ID, "another thread")
	again := dispatch(DispatchRequest{EventID: 1, NoRepo: true, Again: true})
	assert.NotEqual(t, first.ID, again.ID, "--again")
	// The thread's latest worker on the same repo and agent takes the next.
	next := dispatch(DispatchRequest{EventID: 4, NoRepo: true, Spec: Spec{Effort: "high"}})
	assert.Equal(t, again.ID, next.ID)
	assert.Equal(t, "high", next.Effort, "a continued session takes a changed effort")
	assert.Len(t, h.runner.Workers(), 5)
}

// A session its CLI no longer has cannot be continued. The request then
// starts in a new session, with the instructions whole, rather than failing.
func TestARequestStartsFreshWhenTheEarlierSessionIsGone(t *testing.T) {
	for _, agent := range AdapterNames() {
		t.Run(agent, func(t *testing.T) {
			h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: agent} })
			h.settled()
			before := len(by(h.invocations(), agent))
			require.NoError(t, h.runner.Deliver(context.Background(), h.onThread(1, "first")))
			h.settled()
			h.runner.mu.Lock()
			lost := h.runner.st.Workers[0].Session.ID
			h.runner.mu.Unlock()
			require.NotEmpty(t, lost)
			require.NoError(t, os.WriteFile(filepath.Join(h.opts.Dir, "lost-sessions"), []byte(lost), 0o600))

			require.NoError(t, h.runner.Deliver(context.Background(), h.onThread(2, "second")))
			h.settled()
			workers := h.runner.Workers()
			require.Len(t, workers, 1)
			assert.Equal(t, workerDone, workers[0].State)
			assert.Equal(t, 2, workers[0].Requests)
			h.runner.mu.Lock()
			fresh := h.runner.st.Workers[0].Session.ID
			h.runner.mu.Unlock()
			assert.NotEqual(t, lost, fresh)

			// Three turns of the worker's: the first, the one that could not
			// be continued, and the one that started over.
			turns := by(h.invocations(), agent)[before:]
			if agent == "claude" {
				turns = slices.DeleteFunc(turns, func(inv fakeInvocation) bool {
					return !strings.Contains(inv.Prompt, "event_id") || strings.Contains(inv.Prompt, "[basecamp-request]")
				})
			}
			require.Len(t, turns, 3)
			assert.True(t, strings.HasPrefix(turns[1].Prompt, "[connector] A further request"))
			assert.True(t, strings.HasPrefix(turns[2].Prompt, "You are a worker of a Basecamp agent"))
			assert.Contains(t, turns[2].Prompt, `"event_id":2`)
			assert.NotContains(t, strings.Join(turns[2].Args, " "), lost)
		})
	}
}

// A request that arrives while the thread's worker is still on an earlier
// one waits for it, in the same session, instead of running beside it.
func TestARequestWaitsForTheWorkerStillOnItsThread(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "pi"} })
	require.NoError(t, h.runner.Deliver(context.Background(), h.onThread(1, "SLOW-WORKER")))
	require.Eventually(t, func() bool { return h.runner.Status().Running == 1 && h.runner.Status().Waiting == 0 }, 20*time.Second, 20*time.Millisecond)
	require.NoError(t, h.runner.Deliver(context.Background(), h.onThread(2, "meanwhile")))
	require.Eventually(t, func() bool {
		workers := h.runner.Workers()
		return len(workers) == 1 && workers[0].Requests == 2 && h.runner.Status().Waiting == 0
	}, 20*time.Second, 20*time.Millisecond)
	assert.Len(t, by(h.invocations(), "pi"), 1, "the second request has not started a process of its own")
	assert.Equal(t, 1, h.runner.Status().Running)
	_, err := h.runner.StopWorker(h.runner.Workers()[0].ID)
	require.NoError(t, err)
}

func TestAStateFileFromBeforePromptsCarriedMoreThanTextStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), StateFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"workers":[{"id":"w1","state":"queued","session":{"spec":{"adapter":"pi"}},"prompts":["the handoff",{"text":"more","event_id":7}]}]}`), 0o600))
	s, err := loadState(path)
	require.NoError(t, err)
	assert.Equal(t, []prompt{{Text: "the handoff"}, {Text: "more", EventID: 7}}, s.Workers[0].Prompts)
}

// What agents may do is read from runner.json for every turn, so a change
// applies to the next one with no restart, and a file that cannot be read
// starts no agent at all rather than starting one with prompts off.
func TestPermissionsAreReadForEveryTurnAndAnUnreadableFileStartsNoAgent(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "codex"} })
	h.settled()
	orchestratorArgs := func() []string {
		all := by(h.invocations(), "claude")
		return all[len(all)-1].Args
	}
	assert.Contains(t, orchestratorArgs(), "bypassPermissions", "full is the default")
	first := by(h.invocations(), "claude")[0]
	assert.Contains(t, first.Prompt, "[connector] The connector started. It runs as profile agent. Permissions are full.")

	require.NoError(t, SaveStart(h.opts.ConfigPath, Spec{}, Spec{}, PermissionsGuarded))
	_, err := h.runner.Say(context.Background(), "hello")
	require.NoError(t, err)
	assert.Equal(t, "auto", orchestratorArgs()[slices.Index(orchestratorArgs(), "--permission-mode")+1])
	_, _, err = h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	h.settled()
	worker := by(h.invocations(), "codex")
	last := worker[len(worker)-1]
	assert.NotContains(t, last.Args, "--dangerously-bypass-approvals-and-sandbox")
	assert.Contains(t, last.Args, `sandbox_mode="workspace-write"`)
	// A sandboxed worker is told what the sandbox stops and what to do in
	// place of what the skill's instructions assume.
	assert.Contains(t, last.Args, `approvals_reviewer="auto_review"`)
	assert.Contains(t, last.Prompt, "The owner runs you with guarded permissions: Codex's sandbox, with automatic review.")
	assert.Contains(t, last.Prompt, "Ask to run that same command outside the sandbox, once.")
	assert.Less(t, strings.Index(last.Prompt, "Codex's sandbox"), strings.Index(last.Prompt, "SUBAGENT BODY"))

	// A coding agent with no guardrails is refused under guarded, where the
	// refusal can be acted on: at the dispatch.
	_, _, err = h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "x", Spec: Spec{Adapter: "pi"}})
	require.ErrorContains(t, err, "pi has no guardrails of its own")

	// Unreadable: the message waits, and no agent is started meanwhile.
	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{"permissions": "anything"}`), 0o600))
	before := len(h.invocations())
	answered := make(chan error, 1)
	go func() {
		_, err := h.runner.Say(context.Background(), "still there?")
		answered <- err
	}()
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, h.invocations(), before)
	select {
	case err := <-answered:
		t.Fatalf("answered while runner.json could not be read: %v", err)
	default:
	}
	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{}`), 0o600))
	require.NoError(t, <-answered)
	assert.Contains(t, orchestratorArgs(), "bypassPermissions")
}

func TestTheGuardedNoticeSpeaksOnlyOfTheCodingAgentsInUse(t *testing.T) {
	notice := strings.Join(PermissionsNotice(PermissionsGuarded, "agent", "codex", "claude"), "\n")
	assert.Contains(t, notice, "Codex: it runs each command in its sandbox")
	assert.Contains(t, notice, "decides itself, with no one asked")
	assert.Contains(t, notice, "Claude Code: it runs in its auto mode")
	assert.Len(t, PermissionsNotice(PermissionsGuarded, "agent", "claude"), 2)
	assert.Contains(t, PermissionsNotice(PermissionsFull, "it's me")[0], `basecamp connect show -P 'it'\''s me'`)
}

// An orchestrator whose coding agent cannot run under guarded takes no turn
// while guarded is in force, and takes its messages again once it is not.
func TestAnOrchestratorThatCannotRunUnderGuardedWaitsUntilItCan(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Orchestrator = Spec{Adapter: "pi"} })
	h.settled()
	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{"permissions": "guarded"}`), 0o600))
	before := len(h.invocations())
	answered := make(chan error, 1)
	go func() {
		_, err := h.runner.Say(context.Background(), "hello")
		answered <- err
	}()
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, h.invocations(), before, "Pi is not started under a name that promises guardrails it does not have")
	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{}`), 0o600))
	select {
	case err := <-answered:
		// However long it waited, it was not counted as tried and set
		// aside: a turn that was refused reached no agent.
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the message was never taken up again")
	}
	assert.Zero(t, h.runner.Status().SetAside)
}

// Two dispatches for one request, arriving together, give it one worker: the
// second finds the first's, however long the first spent reading what the
// request sits under.
func TestDispatchesForOneRequestArrivingTogetherStartOneWorker(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Recordings = func(context.Context, int64, int64, string) []int64 {
			time.Sleep(100 * time.Millisecond)
			return []int64{789, 5000}
		}
		require.NoError(t, os.WriteFile(o.ConfigPath, []byte(`{"rules": [{"card_table": 5000, "worker": {"effort": "high"}}]}`), 0o600))
	})
	h.settled()
	h.runner.mu.Lock()
	h.runner.st.Requests[1] = request{Project: 456, RecordingID: 789, RecordingType: "Comment"}
	h.runner.mu.Unlock()

	const callers = 6
	existing := make(chan bool, callers)
	for range callers {
		go func() {
			_, was, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 1, NoRepo: true, Handoff: "x"})
			assert.NoError(t, err)
			existing <- was
		}()
	}
	started := 0
	for range callers {
		if !<-existing {
			started++
		}
	}
	assert.Equal(t, 1, started)
	h.settled()
	assert.Len(t, h.runner.Workers(), 1)
}

// What a request sits under is read when a rule first asks, even for a
// request dispatched earlier, when no rule did.
func TestWhatARequestSitsUnderIsReadOnceARuleAsks(t *testing.T) {
	asked := 0
	h := newHarness(t, func(o *Options) {
		o.Recordings = func(context.Context, int64, int64, string) []int64 {
			asked++
			return []int64{789, 5000}
		}
	})
	h.settled()
	h.runner.mu.Lock()
	h.runner.st.Requests[1] = request{Project: 456, RecordingID: 789, RecordingType: "Comment"}
	h.runner.mu.Unlock()
	_, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 1, NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	assert.Zero(t, asked, "no rule asks, so nothing is read")

	require.NoError(t, os.WriteFile(h.opts.ConfigPath, []byte(`{"rules": [{"card_table": 5000, "worker": {"effort": "high"}}]}`), 0o600))
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{EventID: 1, NoRepo: true, Handoff: "x", Again: true})
	require.NoError(t, err)
	assert.Equal(t, 1, asked)
	assert.Equal(t, "high", view.Effort)
	h.settled()
}

// A worker told something more just after it was stopped, before its turn
// has ended, takes it up on the goroutine it already has: its session never
// has two turns at once.
func TestAWorkerToldMoreWhileItsStoppedTurnEndsRunsOneTurnAtATime(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Worker = Spec{Adapter: "codex"} })
	h.settled()
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "HANG"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		h.runner.mu.Lock()
		defer h.runner.mu.Unlock()
		return h.runner.st.Workers[0].State == workerRunning && h.runner.st.Workers[0].Session.ID != ""
	}, 10*time.Second, 20*time.Millisecond)

	_, err = h.runner.StopWorker(view.ID)
	require.NoError(t, err)
	_, err = h.runner.Send(view.ID, "carry on with this instead")
	require.NoError(t, err)
	h.settled()

	workers := h.runner.Workers()
	require.Len(t, workers, 1)
	assert.Equal(t, workerDone, workers[0].State)
	turns := by(h.invocations(), "codex")
	require.Len(t, turns, 2, "the stopped turn, and the one that carried on")
	assert.Equal(t, "[orchestrator] carry on with this instead", turns[1].Prompt)
	h.runner.mu.Lock()
	defer h.runner.mu.Unlock()
	assert.Empty(t, h.runner.working, "its goroutine is gone once it has nothing more to do")
	assert.Empty(t, h.runner.stopping)
}

func TestARequestStillWaitingOrBeingWorkedOnIsNotPruned(t *testing.T) {
	s := &state{Version: stateVersion, Requests: map[int64]request{}}
	for i := range int64(keepRequests + 50) {
		s.Requests[i+1] = request{}
	}
	s.Inbox = []item{{ID: 1, Kind: kindRequest, EventID: 3}}
	s.Workers = []*workerState{
		{ID: "w1", State: workerRunning, EventID: 7, Events: []int64{5, 7}},
		{ID: "w2", State: workerDone, EventID: 9, Events: []int64{9}},
	}
	s.prune()
	for _, id := range []int64{3, 5, 7} {
		assert.Contains(t, s.Requests, id, "request %d is still needed", id)
	}
	assert.NotContains(t, s.Requests, int64(9), "a finished worker's request is history")
	assert.Len(t, s.Requests, keepRequests)
}

// While the connector's hold stands, the runner starts no agent: not for a
// message already in its inbox, and not for a worker waiting its turn.
func TestNoAgentIsStartedWhileTheHoldStands(t *testing.T) {
	var held atomic.Bool
	h := newHarness(t, func(o *Options) {
		o.Held = func(context.Context) (bool, error) { return held.Load(), nil }
	})
	h.settled()
	held.Store(true)
	before := len(h.invocations())
	view, _, err := h.runner.Dispatch(context.Background(), DispatchRequest{NoRepo: true, Handoff: "x"})
	require.NoError(t, err)
	answered := make(chan error, 1)
	go func() {
		_, err := h.runner.Say(context.Background(), "anything?")
		answered <- err
	}()
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, h.invocations(), before, "neither the worker nor the orchestrator was started")
	assert.Equal(t, workerQueued, h.runner.Workers()[0].State)

	held.Store(false)
	require.NoError(t, <-answered)
	h.settled()
	workers := h.runner.Workers()
	assert.Equal(t, view.ID, workers[0].ID)
	assert.Equal(t, workerDone, workers[0].State)
}

// A crashed run can leave a process group whose leader is gone and whose
// other members are still at work. They are ended too, before the worker is
// continued.
func TestWhatALeftoverAgentStartedIsEndedWithIt(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to stand in for an agent that started something and went")
	}
	var process driver.Process
	h := newHarness(t, func(o *Options) {
		gone, err := driver.StartWorker(context.Background(), driver.SessionConfig{Scope: driver.Scope{WorkDir: o.Dir}},
			driver.Command{Path: shell, Args: []string{"-c", "sleep 600 >/dev/null 2>&1 & exit 0"}})
		require.NoError(t, err)
		<-gone.Done()
		process = gone.Process()
		// The runner ends it; this is for a run of the test in which it does not.
		t.Cleanup(func() { _ = driver.ConfirmGroupGone(process, time.Second) })
		require.True(t, driver.GroupMembersRemain(process), "the leader went and left a member of its group running")
		crashed := &state{
			Version: stateVersion, WorkerSeq: 1, Requests: map[int64]request{},
			Orchestrator: sessionState{Spec: o.Orchestrator, Dir: filepath.Join(o.Dir, "orchestrator")},
			Workers: []*workerState{{
				ID: "w1", State: workerRunning, Prompts: []prompt{{Text: "the handoff", Fresh: "the handoff"}},
				Session: sessionState{Spec: Spec{Adapter: "codex"}, ID: "33333333-3333-4333-8333-333333333333", Dir: o.Dir, Process: &process},
			}},
		}
		require.NoError(t, crashed.save(filepath.Join(o.Dir, StateFile)))
	})
	assert.False(t, driver.GroupMembersRemain(process), "what the leader left running is ended before its worker is continued")
	h.settled()
	assert.Equal(t, workerDone, h.runner.Workers()[0].State)
}

// A leftover that cannot be confirmed ended stops the start, whichever
// session it was: a worker that was only set aside could be told more, or
// take a later request on its thread, beside the process nobody ended.
func TestALeftoverThatCannotBeConfirmedEndedStopsTheStart(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep command to stand in for a process")
	}
	for name, crashed := range map[string]func(dir string, p *driver.Process) *state{
		"a worker that had already been stopped": func(dir string, p *driver.Process) *state {
			return &state{
				Version: stateVersion, WorkerSeq: 1, Requests: map[int64]request{},
				Orchestrator: sessionState{Spec: Spec{Adapter: "claude"}, Dir: filepath.Join(dir, "orchestrator")},
				Workers: []*workerState{{
					ID: "w1", State: workerStopped, Prompts: []prompt{{Text: "the handoff", Fresh: "the handoff"}},
					Session: sessionState{Spec: Spec{Adapter: "codex"}, ID: "44444444-4444-4444-8444-444444444444", Dir: dir, Process: p},
				}},
			}
		},
		"the orchestrator": func(dir string, p *driver.Process) *state {
			return &state{
				Version: stateVersion, Requests: map[int64]request{},
				Orchestrator: sessionState{Spec: Spec{Adapter: "claude"}, ID: "old", Dir: filepath.Join(dir, "orchestrator"), Process: p},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel() // each waits out the grace a group is given to go
			var stranger *driver.Worker
			h := unstartedHarness(t, func(o *Options) {
				started, err := driver.StartWorker(context.Background(), driver.SessionConfig{Scope: driver.Scope{WorkDir: o.Dir}},
					driver.Command{Path: sleep, Args: []string{"600"}})
				require.NoError(t, err)
				stranger = started
				// The recorded pid, since given to another process: one that
				// started at another time. Whose group it is cannot be
				// established, so it is neither signaled nor called gone.
				process := stranger.Process()
				process.StartedAt = process.StartedAt.Add(-time.Hour)
				require.NoError(t, crashed(o.Dir, &process).save(filepath.Join(o.Dir, StateFile)))
			})
			t.Cleanup(func() { stranger.Terminate(time.Second) })

			r, err := New(h.opts)
			require.NoError(t, err)
			err = r.Run(t.Context())
			require.ErrorIs(t, err, driver.ErrGroupOutlivedLeader)
			assert.Contains(t, err.Error(), "could not be confirmed ended")
			assert.Empty(t, h.invocations(), "no agent is started beside it")
			select {
			case <-stranger.Done():
				t.Fatal("a process that is not provably the recorded one was ended")
			default:
			}
			kept, err := loadState(filepath.Join(h.opts.Dir, StateFile))
			require.NoError(t, err)
			if len(kept.Workers) > 0 {
				assert.NotNil(t, kept.Workers[0].Session.Process, "its record is kept for the next start")
			} else {
				assert.NotNil(t, kept.Orchestrator.Process, "its record is kept for the next start")
			}
		})
	}
}

// A socket is only called, and only listened on, in a directory that is
// this user's alone and is a directory of its own. The fallback for a path
// too long for a socket address can sit where other users write, and one of
// them could have made it, or a link in its place, first.
func TestASocketIsOnlyUsedInADirectoryThatIsThisUsersAlone(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "private")
	require.NoError(t, os.Mkdir(private, 0o700))
	require.NoError(t, checkSocketDir(private))

	open := filepath.Join(base, "open")
	require.NoError(t, os.Mkdir(open, 0o755)) //nolint:gosec // G301: the case under test
	require.ErrorContains(t, checkSocketDir(open), "can be entered by other users")
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(private, link))
	require.ErrorContains(t, checkSocketDir(link), "is not a directory of its own")
	require.ErrorIs(t, checkSocketDir(filepath.Join(base, "missing")), os.ErrNotExist)

	_, err := listen(context.Background(), filepath.Join(link, "sock"))
	require.ErrorContains(t, err, "is not a directory of its own", "a link is not adopted and given this user's permissions")
	_, err = Call(context.Background(), filepath.Join(open, "sock"), Request{Op: OpWorkers}, time.Second)
	require.ErrorContains(t, err, "can be entered by other users")

	// A path too long for a socket address goes to the user's runtime
	// directory, where there is one, under a name that is the same for the
	// connector and for whoever calls it.
	runtime, err := os.MkdirTemp("", "rt") // short: a test's own directory is too long a path itself
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	long := filepath.Join(base, strings.Repeat("d", 120))
	path := SocketPath(long)
	assert.Equal(t, filepath.Join(runtime, "basecamp-connect"), filepath.Dir(path))
	assert.Equal(t, path, SocketPath(long))
	assert.LessOrEqual(t, len(path), maxSocketPath+30)
	listener, err := listen(context.Background(), path)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	info, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
