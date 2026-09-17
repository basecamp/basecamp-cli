//go:build unix

package connector

import (
	"context"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Dispatch, acknowledgement and completion, with the connector killed at every
// ledger state an event passes through on its way to a worker and back.

// harnessAttempt is an attempts row.
type harnessAttempt struct {
	ID          string
	TaskID      int64
	State       string
	StopReason  string
	SpawnFailed bool
}

func harnessAttempts(t *testing.T, l *Ledger) []harnessAttempt {
	t.Helper()
	rows, err := l.db.QueryContext(context.Background(), `SELECT id, task_id, state, COALESCE(stop_reason, ''), spawn_failed FROM attempts ORDER BY launched_at, rowid`)
	require.NoError(t, err)
	defer rows.Close()
	var out []harnessAttempt
	for rows.Next() {
		var a harnessAttempt
		require.NoError(t, rows.Scan(&a.ID, &a.TaskID, &a.State, &a.StopReason, &a.SpawnFailed))
		out = append(out, a)
	}
	require.NoError(t, rows.Err())
	return out
}

// outcomeOf is the outcome on the event's latest task.
func outcomeOf(t *testing.T, l *Ledger, eventID int64) string {
	t.Helper()
	var outcome string
	require.NoError(t, l.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(outcome, '') FROM task_events WHERE event_id = ? ORDER BY task_id DESC LIMIT 1`, eventID).Scan(&outcome))
	return outcome
}

// handed counts the times a worker was prompted with the event, across every
// agent process of the harness.
func (h *harness) handed(eventID int64) int {
	n := 0
	for _, e := range h.agentLog() {
		if e.Event == eventID && e.Step == "prompt" {
			n++
		}
	}
	return n
}

// agentStarts counts the agent processes that started.
func (h *harness) agentStarts() int {
	n := 0
	for _, e := range h.agentLog() {
		if e.Step == "start" {
			n++
		}
	}
	return n
}

// recordedWorkers is the worker processes the ledger recorded, which is all a
// restart has to end them by.
func recordedWorkers(t *testing.T, l *Ledger) []int {
	t.Helper()
	rows, err := l.db.QueryContext(context.Background(), `SELECT pid FROM attempts WHERE pid IS NOT NULL AND pid > 0`)
	require.NoError(t, err)
	defer rows.Close()
	var out []int
	for rows.Next() {
		var pid int
		require.NoError(t, rows.Scan(&pid))
		out = append(out, pid)
	}
	require.NoError(t, rows.Err())
	return out
}

// notices are the connector's completion notices naming the event.
func (h *harness) notices(eventID int64) []storedMessage {
	var out []storedMessage
	for _, m := range h.connectorPosts() {
		if strings.Contains(m.Content, "automatic notice") && strings.Contains(m.Content, "Event "+strconv.FormatInt(eventID, 10)+":") {
			out = append(out, m)
		}
	}
	return out
}

// processGone says pid no longer runs: it does not exist, or it is a zombie
// nobody has reaped yet.
func processGone(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the parenthesised command name.
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}

// completedWork is a worker that does the whole job.
var completedWork = []string{"get", "ack", "reply", "complete"}

// crashRow is one kill point in an event's life.
type crashRow struct {
	name string
	// kill is where the connector kills itself; empty when the plan's worker
	// kills it.
	kill string
	// plan is the worker's script for the event's first prompt.
	plan []string

	// handed is how many times a worker was given the event, in all.
	handed int
	// outcome is the event's outcome once recovered.
	outcome Outcome
	// stop is the one attempt's stop reason once recovered.
	stop StopReason
	// notices is how many completion notices name the event.
	notices int
	// indeterminate is how many lifecycle messages wait for a person.
	indeterminate int
	// race marks the rows that also run under the race detector.
	race bool
}

var crashRows = []crashRow{
	{name: "seen, the read in flight", kill: "read:5001", plan: completedWork,
		handed: 1, outcome: OutcomeSucceeded, stop: StopFinished},
	{name: "seen, the verdict not committed", kill: "tx:verdict", plan: completedWork,
		handed: 1, outcome: OutcomeSucceeded, stop: StopFinished},
	{name: "admitted", kill: "line:event:admitted", plan: completedWork,
		handed: 1, outcome: OutcomeSucceeded, stop: StopFinished},
	{name: "dispatched, attempt launching", kill: "line:dispatch:launching", plan: completedWork,
		handed: 0, outcome: OutcomeUnknown, stop: StopLost, notices: 1},
	{name: "dispatched, attempt running before the prompt", kill: "line:dispatch:running", plan: completedWork,
		handed: 0, outcome: OutcomeUnknown, stop: StopLost, notices: 1},
	{name: "exposed by get_dispatch", plan: []string{"get", "kill", "linger"},
		handed: 1, outcome: OutcomeUnknown, stop: StopLost, notices: 1, race: true},
	{name: "delivered by ack_dispatch", plan: []string{"get", "ack", "kill", "linger"},
		handed: 1, outcome: OutcomeUnknown, stop: StopLost, notices: 1},
	{name: "completed by complete_dispatch", plan: []string{"get", "ack", "reply", "complete", "kill", "linger"},
		handed: 1, outcome: OutcomeSucceeded, stop: StopLost},
	{name: "worker gone, settlement not committed", kill: "tx:attempt-ended", plan: completedWork,
		handed: 1, outcome: OutcomeSucceeded, stop: StopLost},
	{name: "settled, completion notice due", kill: "line:dispatch:ended", plan: []string{"get", "ack", "fail"},
		handed: 1, outcome: OutcomeFailed, stop: StopFinished, notices: 1},
	{name: "completion notice sending, not posted", kill: "post-before", plan: []string{"get", "ack", "fail"},
		handed: 1, outcome: OutcomeFailed, stop: StopFinished, indeterminate: 1},
	{name: "completion notice posted, receipt not recorded", kill: "post-after", plan: []string{"get", "ack", "fail"},
		handed: 1, outcome: OutcomeFailed, stop: StopFinished, notices: 1, race: true},
}

// Recovery never re-runs an instruction a worker may have seen, never resends
// a lifecycle message, and leaves an intent it cannot settle indeterminate and
// visible; everything the crash interrupted before a worker existed runs once.
func TestRecoveryAtEveryLedgerState(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		for _, row := range crashRows {
			t.Run(row.name, func(t *testing.T) {
				raceSubset(t, row.race)
				h := newHarness(t, d, harnessScenario{Plans: map[string][]string{"101#1": row.plan}})
				h.publish(feedEntry{Event: todoEvent(101, 5001)})
				h.run(harnessRun{Kill: row.kill, Killed: true})
				if kind, ok := strings.CutPrefix(row.kill, "line:"); ok {
					lines := h.lines()
					require.NotEmpty(t, lines)
					last := lines[len(lines)-1]
					assert.Equal(t, kind, last.Type+":"+last.State, "the connector's last word was the line it was killed at")
				}
				var lingering []int
				if slices.Contains(row.plan, "linger") {
					// The crash left a worker running: it is the restart's to
					// end, by the process group the ledger recorded.
					lingering = recordedWorkers(t, h.ledger())
					require.NotEmpty(t, lingering, "the crash left a worker the ledger recorded")
					for _, pid := range lingering {
						assert.False(t, processGone(pid), "the worker outlived the connector, pid %d", pid)
					}
				}

				h.run(harnessRun{})
				h.assertRecovered(row)
				for _, pid := range lingering {
					assert.True(t, processGone(pid), "the restart ended the worker the crash left, pid %d", pid)
				}

				// A second restart finds nothing to do and sends nothing.
				posts := len(h.connectorPosts())
				h.run(harnessRun{})
				h.assertRecovered(row)
				assert.Len(t, h.connectorPosts(), posts, "recovery never resends a lifecycle message")
			})
		}
	})
}

func (h *harness) assertRecovered(row crashRow) {
	t := h.t
	t.Helper()
	l := h.ledger()
	assert.Equal(t, row.handed, h.handed(101), "times a worker was given the event")
	assert.Equal(t, StateCompleted, stateOf(t, l, 101))
	assert.Equal(t, string(row.outcome), outcomeOf(t, l, 101))
	attempts := harnessAttempts(t, l)
	if assert.Len(t, attempts, 1, "no second attempt") {
		assert.Equal(t, string(row.stop), attempts[0].StopReason)
	}
	assert.Len(t, h.notices(101), row.notices, "completion notices")

	status, err := l.Status(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, status.Indeterminate, row.indeterminate, "indeterminate lifecycle messages in status")
	for _, in := range status.Indeterminate {
		assert.Equal(t, string(IntentCompletion), in.Kind)
	}
}

// An unknown outcome is posted as needing a person, and a person's redispatch
// runs the event again, once.
func TestRecoveryPostsUnknownAndRedispatchRunsItAgain(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		h := newHarness(t, d, harnessScenario{Plans: map[string][]string{"101#1": {"get", "ack", "kill", "linger"}}})
		h.publish(feedEntry{Event: todoEvent(101, 5001)})
		h.run(harnessRun{Killed: true})
		h.run(harnessRun{})

		notices := h.notices(101)
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0].Content, "Event 101: unknown")
		assert.Contains(t, notices[0].Content, "basecamp connect redispatch 101")
		assert.Equal(t, int64(5001), notices[0].RecordingID, "on the recording that asked")

		l := h.ledger()
		got, err := l.Redispatch(context.Background(), 101, "operator")
		require.NoError(t, err)
		assert.False(t, got.Held)
		h.run(harnessRun{})

		assert.Equal(t, 2, h.handed(101), "once before the crash, once by the redispatch")
		assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, 101))
		attempts := harnessAttempts(t, l)
		require.Len(t, attempts, 2)
		assert.NotEqual(t, attempts[0].TaskID, attempts[1].TaskID, "a redispatch is a new task")
		assert.Equal(t, string(StopFinished), attempts[1].StopReason)
		assert.Len(t, h.notices(101), 1, "the redispatch succeeded with a reply: no second notice")

		h.run(harnessRun{})
		assert.Equal(t, 2, h.handed(101), "a restart after the redispatch runs nothing again")
	})
}

// A start that ran nothing is retried once, across a restart as well, and a
// second failure blocks the record; a start whose process existed is never
// retried.
func TestRecoveryRetriesASpawnErrorOnce(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		t.Run("twice in one run", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{SpawnFail: 2})
			l := h.ledger()
			assertSpawnBlocked(t, h, l)
		})
		t.Run("the retry after a restart runs", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{SpawnFail: 1, Kill: "line:dispatch:ended", Killed: true})
			l := h.ledger()
			assert.Equal(t, StateAdmitted, stateOf(t, l, 101), "the withdrawn exposure is durable")

			h.run(harnessRun{})
			assert.Equal(t, 1, h.handed(101))
			assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, 101))
			attempts := harnessAttempts(t, l)
			require.Len(t, attempts, 2)
			assert.True(t, attempts[0].SpawnFailed)
			assert.False(t, attempts[1].SpawnFailed)
		})
		t.Run("the retry budget survives a restart", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{SpawnFail: 1, Kill: "line:dispatch:ended", Killed: true})
			h.run(harnessRun{SpawnFail: 1})
			l := h.ledger()
			assertSpawnBlocked(t, h, l)
			h.run(harnessRun{})
			assert.Len(t, harnessAttempts(t, l), 2, "a blocked record is not started again by a restart")
		})
		t.Run("a start that failed its handshake", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{BadModeStarts: 1})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{})
			h.run(harnessRun{})
			l := h.ledger()
			assert.Equal(t, StateCompleted, stateOf(t, l, 101))
			assert.Equal(t, string(OutcomeUnknown), outcomeOf(t, l, 101), "a process existed: it may have acted")
			attempts := harnessAttempts(t, l)
			require.Len(t, attempts, 1, "never retried")
			assert.False(t, attempts[0].SpawnFailed)
			assert.Equal(t, string(StopFailed), attempts[0].StopReason)
			assert.Equal(t, 1, h.agentStarts())
			assert.Len(t, h.notices(101), 1)
		})
	})
}

func assertSpawnBlocked(t *testing.T, h *harness, l *Ledger) {
	t.Helper()
	record := getRecord(t, l, 101)
	assert.Equal(t, StateBlocked, record.State)
	assert.Equal(t, ReasonSpawnFailed, record.Reason)
	attempts := harnessAttempts(t, l)
	require.Len(t, attempts, 2, "one automatic retry, no third start")
	for _, a := range attempts {
		assert.True(t, a.SpawnFailed)
	}
	assert.Zero(t, h.agentStarts(), "no worker process ever existed")
	notices := h.notices(101)
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0].Content, "could not be started")
}

// Follow-ups and their siblings survive a task's end, a crash included: each
// event is settled on its own, an event never handed to a worker waits for a
// task of its own, and an exposed one is never run again.
func TestRecoveryFollowUpsSurviveTheirTasksEnd(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		t.Run("a follow-up arrives, the task ends", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{Plans: map[string][]string{
				"101#1": {"get", "arrive:102", "await:102=dispatched", "ack", "reply", "complete"},
			}})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{})
			l := h.ledger()
			for _, id := range []int64{101, 102} {
				assert.Equal(t, 1, h.handed(id), "event %d", id)
				assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, id), "event %d", id)
			}
			assert.Empty(t, h.connectorPosts())
		})
		t.Run("the connector dies before the follow-up is handed over", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{Plans: map[string][]string{
				"101#1": {"get", "arrive:102", "await:102=dispatched", "kill", "linger"},
			}})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{Killed: true})
			h.run(harnessRun{})
			l := h.ledger()
			assert.Equal(t, 1, h.handed(101))
			assert.Equal(t, string(OutcomeUnknown), outcomeOf(t, l, 101))
			assert.Equal(t, 1, h.handed(102), "the follow-up was never exposed, so it runs as a task of its own")
			assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, 102))
			assert.Len(t, harnessAttempts(t, l), 2)
			assert.Len(t, h.notices(101), 1)
			assert.Empty(t, h.notices(102))
		})
		t.Run("the connector dies with the follow-up exposed", func(t *testing.T) {
			h := newHarness(t, d, harnessScenario{Plans: map[string][]string{
				"101#1": {"get", "arrive:102", "await:102=dispatched", "ack", "reply", "complete"},
				"102#1": {"get", "kill", "linger"},
			}})
			h.publish(feedEntry{Event: todoEvent(101, 5001)})
			h.run(harnessRun{Killed: true})
			h.run(harnessRun{})
			l := h.ledger()
			assert.Equal(t, 1, h.handed(101))
			assert.Equal(t, 1, h.handed(102), "exposed: never run again")
			assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, 101), "a reported outcome stands")
			assert.Equal(t, StateCompleted, stateOf(t, l, 102))
			assert.Equal(t, string(OutcomeUnknown), outcomeOf(t, l, 102))
			assert.Empty(t, h.notices(101))
			assert.Len(t, h.notices(102), 1)
		})
	})
}

// measuredTokenizerRatio is how far estimateTokens undercounts a real
// tokenizer on the dispatch prompt: Claude Opus 5 counted the production-sized
// prompt below at 322 tokens where the estimate says 230 (measured with
// Claude Code's reported input usage, against the same session with a
// one-character prompt). The budget is asserted on the estimate scaled by it.
const measuredTokenizerRatio = 1.5

// The dispatch prompt is measured as the worker received it, through each
// driver's wire, at production-sized ids, and at its worst case: the largest
// ids and the longest recording URL the prompt repeats.
func TestRecoveryTheDispatchPromptIsUnderBudget(t *testing.T) {
	const (
		event     = int64(17_099_838_500)
		followUp  = int64(17_099_838_501)
		recording = int64(10_304_029_146)
	)
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		h := newHarness(t, d, harnessScenario{Plans: map[string][]string{
			strconv.FormatInt(event, 10) + "#1": {"get", "arrive:" + strconv.FormatInt(followUp, 10), "await:" + strconv.FormatInt(followUp, 10) + "=dispatched", "ack", "reply", "complete"},
		}})
		h.publish(feedEntry{Event: todoEvent(event, recording)})
		h.run(harnessRun{})

		prompts := map[int64]string{}
		for _, e := range h.agentLog() {
			if e.Step == "prompt" {
				prompts[e.Event] = e.Prompt
			}
		}
		require.Contains(t, prompts, event)
		require.Contains(t, prompts, followUp)
		for id, prompt := range prompts {
			tokens := estimateTokens(prompt)
			t.Logf("%s: prompt for event %d: %d bytes, %d tokens estimated, %d scaled to a real tokenizer, budget %d",
				d.Name, id, len(prompt), tokens, int(float64(tokens)*measuredTokenizerRatio), MaxPromptTokens)
			assert.Less(t, float64(tokens)*measuredTokenizerRatio, float64(MaxPromptTokens))
			assert.NotContains(t, prompt, "please do the thing", "no content in the prompt")
		}
		if out := os.Getenv("BASECAMP_RECOVERY_PROMPT_OUT"); out != "" {
			require.NoError(t, os.WriteFile(out, []byte(prompts[event]), 0o600))
		}
	})

	t.Run("worst case", func(t *testing.T) {
		longest := "https://app.basecamp.com/" + strings.Repeat("9", 200-len("https://app.basecamp.com/"))
		record := Record{ID: math.MaxInt64, Decision: Decision{Trigger: "completed", RecordingURL: longest}}
		prompt := DispatchPrompt(Launch{TaskID: math.MaxInt64}, record)
		require.Contains(t, prompt, longest, "the longest URL the prompt repeats")
		tokens := estimateTokens(prompt)
		t.Logf("worst-case dispatch prompt: %d bytes, %d tokens estimated, %d scaled to a real tokenizer, budget %d",
			len(prompt), tokens, int(float64(tokens)*measuredTokenizerRatio), MaxPromptTokens)
		assert.Less(t, float64(tokens)*measuredTokenizerRatio, float64(MaxPromptTokens))
		followUp := FollowUpPrompt(math.MaxInt64)
		assert.Less(t, float64(estimateTokens(followUp))*measuredTokenizerRatio, float64(MaxPromptTokens))
	})
}
