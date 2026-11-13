package console

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

var testNow = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func before(d time.Duration) *time.Time {
	at := testNow.Add(-d)
	return &at
}

// running is a connector doing the work itself, with one worker at work and
// one done.
func running() *Snapshot {
	return &Snapshot{
		At: testNow, Profile: "agent", AccountID: "1",
		Agent: Person{ID: 9, Name: "R2-D2"},
		Connector: Connector{Running: true, PID: 4000, StartedAt: testNow.Add(-2 * time.Hour).Format(time.RFC3339),
			LastChange: &connector.ConnectionStatus{State: connector.ConnectionRunning, PID: 4000, ChangedAt: testNow.Add(-2 * time.Hour)}},
		Projects: []Project{{ID: 456, Name: "Launch", Campfires: 1}},
		Trust:    Trust{Mode: "operator", Operator: Person{ID: 7, Name: "Ann"}},
		Runner: &Runner{Live: true, Orchestrator: runner.Spec{Adapter: "claude", Model: "opus"}, Worker: runner.Spec{Adapter: "codex"},
			Permissions: "full", Turns: 12,
			Rules: []Rule{{When: []string{"asked in Launch"}, Worker: runner.Spec{Adapter: "pi"}}}},
		Now: []runner.WorkerActivity{{
			WorkerView: runner.WorkerView{ID: "w2", EventID: 7, State: "running", Adapter: "claude", Model: "opus", Doing: "Bash", StartedAt: before(72 * time.Second), Requests: 1},
			Request:    &runner.RequestSummary{Project: 456, ProjectName: "Launch", Type: "Kanban::Card", Title: "Ship it", URL: "https://app.basecamp.com/1/buckets/456/card_tables/cards/789", Trigger: "mentioned", Requester: "Ann"},
		}},
		Done: []runner.WorkerActivity{{
			WorkerView: runner.WorkerView{ID: "w1", EventID: 6, State: "done", Adapter: "codex", Report: "Posted the answer.", EndedAt: before(5 * time.Minute), Requests: 2},
			Request:    &runner.RequestSummary{Ping: true, Type: "Chat::Lines::Text", Title: "how many open cards", Trigger: "pinged", Requester: "Ann"},
		}},
		Requests: []Request{{EventID: 7, Trigger: "mentioned", EventType: "comment.created", ProjectID: 456, ProjectName: "Launch",
			URL: "https://app.basecamp.com/1/buckets/456/card_tables/cards/789", State: "discarded", Reason: connector.ReasonHandedOff, At: testNow.Add(-2 * time.Minute)}},
		Held: []connector.HeldStatus{},
	}
}

// open is a console showing snap in a window of the given size.
func open(t *testing.T, snap *Snapshot, opts Options, width, height int) model {
	t.Helper()
	opts.Theme = tui.NoColorTheme()
	m := newModel(context.Background(), opts)
	m.now = func() time.Time { return testNow }
	m = step(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	return step(t, m, snapshotMsg{snap})
}

func step(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(model) //nolint:errcheck // the console's own model
}

func press(t *testing.T, m model, keys ...string) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, key := range keys {
		msg := tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
		switch key {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		}
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(model) //nolint:errcheck // the console's own model
	}
	return m, cmd
}

// screen is what the console draws, without its styling.
func screen(m model) string {
	return ansi.Strip(m.render())
}

func TestTheActivityScreenSaysWhatIsWorkedOnAndWhatWasDone(t *testing.T) {
	m := open(t, running(), Options{}, 110, 30)
	shown := screen(m)

	assert.Contains(t, shown, "Basecamp connector · agent")
	assert.Contains(t, shown, "✓ Running · up 2h 00m")
	assert.Contains(t, shown, "● w2  Ship it · Launch  Bash")
	assert.Contains(t, shown, "Claude Code · opus · Working 1m 12s")
	assert.Contains(t, shown, "✓ w1  how many open cards · Ping")
	assert.Contains(t, shown, "Codex · Done 5m ago")
	assert.Contains(t, shown, "✓ Mentioned · comment · Launch")
	assert.Contains(t, shown, "Handed off · 2m ago")
}

// The orchestrator's turn comes before any worker's, so it shows as work
// too: a request that has just come in is otherwise nowhere on the screen.
func TestTheOrchestratorsTurnShowsAsWork(t *testing.T) {
	snap := running()
	snap.Now = nil
	m := open(t, snap, Options{}, 110, 30)
	assert.Contains(t, screen(m), "Nothing is being worked on.")
	assert.NotContains(t, screen(m), "Orchestrator  ")

	snap.Runner.Process, snap.Runner.Waiting = 4242, 2
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "● Orchestrator  at work, with 2 messages not yet handled")
	assert.Contains(t, screen(m), "Claude Code · opus")
	assert.NotContains(t, screen(m), "Nothing is being worked on.")

	snap.Runner.Waiting = 0
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "● Orchestrator  at work")

	// Between turns, with something still to read: the moment before a
	// turn starts, or the wait before a failed one is tried again.
	snap.Runner.Process, snap.Runner.Waiting = 0, 1
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "● Orchestrator  1 message waiting for its next turn")

	// It is a line, not a row: the selection stays on the rows.
	first := m.items()[0].key()
	m, _ = press(t, m, "down", "up")
	assert.Equal(t, first, m.items()[m.cursor].key())
	assert.Equal(t, first, m.selected)

	snap.Runner.Live = false
	m = step(t, m, snapshotMsg{snap})
	assert.NotContains(t, screen(m), "Orchestrator  1 message")
}

func TestARowOpensInFull(t *testing.T) {
	m := open(t, running(), Options{}, 110, 30)
	m, _ = press(t, m, "down", "enter")
	shown := screen(m)

	assert.Contains(t, shown, "how many open cards · Ping")
	assert.Contains(t, shown, "Worker        w1 · ✓ Done")
	assert.Contains(t, shown, "Request       Pinged by Ann on a chat line")
	assert.Contains(t, shown, "Requests      2 on this thread")
	assert.Contains(t, shown, "Posted the answer.")
	assert.NotContains(t, shown, "Ship it", "the list gives way to the row")

	m, _ = press(t, m, "esc")
	assert.Contains(t, screen(m), "Ship it")
}

// A title is whatever someone in Basecamp wrote and a report is whatever a
// coding agent wrote. Neither may reach the terminal as anything but text.
func TestWhatOthersWroteCannotDriveTheTerminal(t *testing.T) {
	const hostile = "\x1b]0;owned\x07\x1b[2J\u009b31mtitle\r\nmore"
	snap := running()
	snap.Now[0].Request.Title = hostile
	snap.Now[0].Request.ProjectName = hostile
	snap.Now[0].Doing = hostile
	snap.Done[0].Report = hostile
	snap.Done[0].Error = hostile
	snap.Requests[0].ProjectName = hostile
	snap.Projects[0].Name = hostile
	snap.Trust.Operator.Name = hostile
	snap.Runner.Rules[0].When = []string{hostile}
	snap.Notes = []string{hostile}
	m := open(t, snap, Options{Say: func(context.Context, string) (string, error) { return hostile, nil }}, 110, 40)
	m.health = &Health{At: testNow, Checks: []Check{{Name: hostile, Status: StatusFail, Message: hostile, Hint: hostile}},
		Agents: []runner.Installed{{Name: "claude", Path: hostile, Version: hostile}}}
	m.talk = []exchange{{said: "hello", reply: hostile}}

	drawn := make([]string, 0, int(tabCount)+2)
	for range tabCount {
		drawn = append(drawn, m.render())
		m, _ = press(t, m, "tab")
	}
	for _, row := range []string{"enter", "down"} {
		m, _ = press(t, m, row)
		drawn = append(drawn, m.render())
	}
	for _, shown := range drawn {
		assert.NotContains(t, shown, "\x1b]", "no operating system command")
		assert.NotContains(t, shown, "\x1b[2J", "no clearing of the screen")
		assert.NotContains(t, shown, "\x07")
		assert.NotContains(t, shown, "\u009b")
		assert.NotContains(t, shown, "\r")
	}
	assert.Contains(t, ansi.Strip(drawn[0]), "title more", "the words themselves still show")
}

func TestNothingIsDrawnPastTheWindow(t *testing.T) {
	snap := running()
	for range 40 {
		snap.Requests = append(snap.Requests, snap.Requests[0])
	}
	say := func(context.Context, string) (string, error) { return "", nil }
	for _, size := range [][2]int{{20, 8}, {40, 12}, {80, 24}, {200, 60}} {
		m := open(t, snap, Options{Say: say}, size[0], size[1])
		m.health = &Health{At: testNow, Checks: []Check{{Name: "Token", Status: StatusPass, Message: strings.Repeat("long ", 60), Hint: strings.Repeat("hint ", 60)}}}
		m.talk = []exchange{{said: strings.Repeat("words ", 80), reply: strings.Repeat("answer ", 200)}}
		for range tabCount {
			lines := strings.Split(m.render(), "\n")
			assert.Len(t, lines, size[1], "%dx%d: every line of the window is drawn, and no more", size[0], size[1])
			for _, line := range lines {
				assert.LessOrEqual(t, ansi.StringWidth(line), size[0], "%dx%d: %q", size[0], size[1], ansi.Strip(line))
			}
			m, _ = press(t, m, "tab")
		}
	}
}

func TestTheSelectionFollowsItsRowWhenTheListChanges(t *testing.T) {
	m := open(t, running(), Options{}, 110, 30)
	m, _ = press(t, m, "down")
	require.Equal(t, "ww1", m.selected)

	// A new worker starts, above the one selected.
	snap := running()
	snap.Now = append([]runner.WorkerActivity{{WorkerView: runner.WorkerView{ID: "w3", State: "queued", Adapter: "pi"}}}, snap.Now...)
	m = step(t, m, snapshotMsg{snap})
	assert.Equal(t, 2, m.cursor)
	assert.Equal(t, "ww1", m.items()[m.cursor].key())

	// The row shown in full is gone: the list comes back.
	m, _ = press(t, m, "enter")
	require.True(t, m.detail)
	snap = running()
	snap.Done = nil
	m = step(t, m, snapshotMsg{snap})
	assert.False(t, m.detail)
	assert.Less(t, m.cursor, len(m.items()))
}

func TestTheSelectedRowStaysInView(t *testing.T) {
	snap := running()
	for i := range 40 {
		r := snap.Requests[0]
		r.EventID = int64(100 + i)
		snap.Requests = append(snap.Requests, r)
	}
	m := open(t, snap, Options{}, 100, 14)
	for range 30 {
		m, _ = press(t, m, "down")
	}
	lines := strings.Split(screen(m), "\n")
	var selected int
	for _, line := range lines {
		if strings.Contains(line, "▸") {
			selected++
		}
	}
	assert.Equal(t, 1, selected, "the selected row is on the screen:\n%s", screen(m))
}

func TestAMessageGoesToTheOrchestratorAndItsAnswerShows(t *testing.T) {
	var told []string
	say := func(_ context.Context, message string) (string, error) {
		told = append(told, message)
		return "Two workers are running.", nil
	}
	m := open(t, running(), Options{Say: say}, 110, 30)
	assert.Contains(t, screen(m), "press / to tell the orchestrator something")

	m, _ = press(t, m, "/")
	require.True(t, m.typing)
	// A key that is a command elsewhere is a letter here.
	m, cmd := press(t, m, "q", "?", "enter")
	require.NotNil(t, cmd)
	assert.Contains(t, screen(m), "You  q?")
	assert.Contains(t, screen(m), "Orchestrator  waiting for its turn")

	m = step(t, m, cmd())
	assert.Equal(t, []string{"q?"}, told)
	assert.Contains(t, screen(m), "Orchestrator  Two workers are running.")

	// An empty message is not sent.
	_, cmd = press(t, m, "enter")
	assert.Nil(t, cmd)
}

// Under the other screens only the end of the conversation fits. The Talk
// screen has all of it, and scrolls.
func TestALongAnswerCanBeReadInFull(t *testing.T) {
	long := "FIRST-WORDS " + strings.Repeat("and more ", 400) + "LAST-WORDS"
	say := func(context.Context, string) (string, error) { return long, nil }
	m := open(t, running(), Options{Say: say}, 80, 24)
	m, _ = press(t, m, "/")
	m, cmd := press(t, m, "h", "i", "enter")
	m = step(t, m, cmd())
	m, _ = press(t, m, "esc")
	shown := screen(m)
	assert.Contains(t, shown, "── Orchestrator · 4 shows all of it ─")
	assert.Contains(t, shown, "LAST-WORDS")
	assert.NotContains(t, shown, "FIRST-WORDS")

	// The Talk screen opens on the end of what was just said, and goes back
	// to its start.
	m, _ = press(t, m, "4")
	shown = screen(m)
	assert.Contains(t, shown, "LAST-WORDS")
	assert.NotContains(t, shown, "── Orchestrator", "the screen is the conversation, so its end is not shown twice")
	m, _ = press(t, m, "k")
	assert.NotContains(t, screen(m), "LAST-WORDS", "one line up from the end")
	m, _ = press(t, m, "g")
	shown = screen(m)
	assert.Contains(t, shown, "You  hi")
	assert.Contains(t, shown, "Orchestrator  FIRST-WORDS")
	m, _ = press(t, m, "G")
	assert.Contains(t, screen(m), "LAST-WORDS")

	// Before anything is said it says how to.
	empty := open(t, running(), Options{Say: say}, 80, 24)
	empty, _ = press(t, empty, "4")
	assert.Contains(t, screen(empty), "Nothing has been said yet. Press / to tell")
	snap := running()
	snap.Runner.Live = false
	empty = step(t, empty, snapshotMsg{snap})
	assert.Contains(t, screen(empty), "no orchestrator is running to talk to")
	assert.NotContains(t, screen(empty), "Press / to tell")
}

// A screen with no row to select scrolls, so what it says can be read to
// its end in a small window: the command that starts an orchestrator is the
// last of it.
func TestAnActivityScreenWithNoRowsScrolls(t *testing.T) {
	snap := running()
	snap.Runner.Live = false
	snap.Now, snap.Done, snap.Requests = nil, nil, nil
	m := open(t, snap, Options{}, 40, 12)
	require.Empty(t, m.items())
	assert.NotContains(t, screen(m), "--run")
	m, _ = press(t, m, "G")
	assert.Contains(t, screen(m), "--run")
	m, _ = press(t, m, "g")
	assert.Contains(t, screen(m), "Working now")
}

// The first row is shown even when what is over it fills the window, and
// up from it is what is over it: the window cannot show both.
func TestTheFirstRowShowsUnderALongExplanation(t *testing.T) {
	snap := running()
	snap.Runner.Live = false
	snap.Now, snap.Done = nil, nil
	m := open(t, snap, Options{}, 40, 12)
	require.Len(t, m.items(), 1)
	assert.Contains(t, screen(m), "▸")
	assert.NotContains(t, screen(m), "Working now")

	m, _ = press(t, m, "g")
	assert.Contains(t, screen(m), "Working now")
	assert.NotContains(t, screen(m), "▸")
	// A snapshot does not take the person back down to the row.
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "Working now")
	m, _ = press(t, m, "j")
	assert.Contains(t, screen(m), "▸")
	assert.Equal(t, 0, m.cursor, "down from there is back to the row, not past it")

	// Opening the row and closing it again is back at the row, and a jump
	// to the end goes to the end.
	m, _ = press(t, m, "g")
	require.True(t, m.above)
	m, _ = press(t, m, "enter", "esc")
	assert.False(t, m.above)
	assert.Contains(t, screen(m), "▸")
	m, _ = press(t, m, "g")
	require.True(t, m.above)
	snap.Requests = append(snap.Requests, snap.Requests[0], snap.Requests[0])
	snap.Requests[1].EventID, snap.Requests[2].EventID = 8, 9
	m = step(t, m, snapshotMsg{snap})
	m, _ = press(t, m, "G")
	assert.False(t, m.above)
	assert.Equal(t, len(m.items())-1, m.cursor)
}

// In a narrow window a row keeps what it is and gives up when: a worker's
// name and what it was asked matter more than the coding agent it runs on.
func TestANarrowRowKeepsItsName(t *testing.T) {
	m := open(t, running(), Options{}, 44, 30)
	shown := screen(m)
	assert.Contains(t, shown, "w2  Ship it")
	assert.Contains(t, shown, "Working 1m 12s")
	assert.NotContains(t, shown, "Claude Code · opus · Working")

	m = open(t, running(), Options{}, 20, 30)
	shown = screen(m)
	assert.Contains(t, shown, "● w2  Ship it")
	assert.Contains(t, shown, "✓ w1  how man")
}

// --run has nothing to start with until a start has kept an orchestrator,
// so a profile that never had one is told to name one.
func TestTheStartCommandIsOneThatWorks(t *testing.T) {
	snap := running()
	snap.Connector = Connector{}
	snap.Runner.Live = false
	m := open(t, snap, Options{}, 110, 40)
	m, _ = press(t, m, "2")
	assert.Contains(t, screen(m), "Start it: basecamp connect -P agent --run")

	snap.Runner.Orchestrator = runner.Spec{}
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "Start it: basecamp connect -P agent --orchestrator claude")
	assert.NotContains(t, screen(m), "--run")
}

// In the narrowest window the checks are still there, each under its name.
func TestHealthIsReadableInTheNarrowestWindow(t *testing.T) {
	m := open(t, running(), Options{Health: func(context.Context) *Health { return nil }}, 20, 30)
	m = step(t, m, healthMsg{&Health{At: testNow, Checks: []Check{{Name: "Token", Status: StatusFail, Message: "Refused", Hint: "Sign in"}}}})
	m, _ = press(t, m, "2")
	shown := screen(m)
	assert.Contains(t, shown, "✗ Token")
	assert.Contains(t, shown, "Refused")
	assert.Contains(t, shown, "Sign in")
}

func TestARequestAPersonClosedSaysSo(t *testing.T) {
	word, _ := outcome(Request{State: "discarded", Reason: connector.ReasonByOperator})
	assert.Equal(t, "Closed without running", word)
}

// A completed record may have failed, and how it ended is not carried, so
// it is not shown as a success.
func TestACompletedRequestIsNotCalledASuccess(t *testing.T) {
	word, tone := outcome(Request{State: "completed"})
	assert.Equal(t, "Finished", word)
	assert.Equal(t, toneNone, tone)
}

func TestAMessageWhoseAnswerIsLostSaysSo(t *testing.T) {
	say := func(context.Context, string) (string, error) { return "", errors.New("the connector stopped") }
	m := open(t, running(), Options{Say: say}, 110, 30)
	m, cmd := press(t, m, "/", "x", "enter")
	m = step(t, m, cmd())
	assert.Contains(t, screen(m), "No reply  the connector stopped")
}

func TestNobodyIsToldWhenNoOrchestratorRuns(t *testing.T) {
	snap := running()
	snap.Runner.Live = false
	m := open(t, snap, Options{Say: func(context.Context, string) (string, error) { return "", nil }}, 110, 30)
	assert.Contains(t, screen(m), "no orchestrator is running to talk to")

	m, cmd := press(t, m, "/")
	assert.False(t, m.typing)
	assert.Nil(t, cmd)
	assert.Contains(t, screen(m), "No connector is running with an orchestrator")
}

// A connector started while the console is open shows as it comes up.
func TestAConnectorShowsAsItComesUp(t *testing.T) {
	snap := running()
	snap.Connector = Connector{LastChange: &connector.ConnectionStatus{State: connector.ConnectionStopped, PID: 3000, ChangedAt: testNow.Add(-time.Hour)}}
	snap.Runner.Live = false
	snap.Now[0].State = "interrupted"
	m := open(t, snap, Options{}, 110, 30)
	assert.Contains(t, screen(m), "– Not running")
	assert.Contains(t, screen(m), "Waiting for the connector")
	m, _ = press(t, m, "2")
	assert.Contains(t, screen(m), "No connector is running for this profile. The last one stopped 1h ago.")

	// It has the lock, and has not said it runs yet: the record is still
	// the last run's.
	snap.Connector.Running, snap.Connector.PID, snap.Connector.StartedAt = true, 4000, testNow.Format(time.RFC3339)
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "● Starting")
	assert.Contains(t, screen(m), "● Basecamp          Starting")
	assert.Contains(t, screen(m), "● Orchestrator      Not answering yet.")

	// Its parts are started, and its orchestrator does not answer: for all
	// the console can tell, the session that started it does the work.
	snap.Connector.LastChange = &connector.ConnectionStatus{State: connector.ConnectionRunning, PID: 4000, ChangedAt: testNow}
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "✓ Listening")
	assert.Contains(t, screen(m), "✓ Listening · no orchestrator")
	assert.Contains(t, screen(m), "– Orchestrator      None: it was started without one")

	snap.Runner.Live = true
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "✓ Running")
	assert.Contains(t, screen(m), "✓ Orchestrator      Answering on Claude Code · opus · 12 turns")
}

func TestHealthSaysWhatIsWrongAndTheTabSaysThereIsSomething(t *testing.T) {
	snap := running()
	m := open(t, snap, Options{Health: func(context.Context) *Health { return nil }}, 110, 40)
	well := &Health{At: testNow.Add(-time.Minute), Checks: []Check{{Name: "Token", Status: StatusPass, Message: "The profile's credential yields a token"}},
		Agents: []runner.Installed{
			{Name: "claude", Path: "/bin/claude", Version: "2.1.0 (Claude Code)", Guarded: true},
			{Name: "codex", Path: "/bin/codex", Version: "codex-cli 0.1"},
			{Name: "pi", Path: "/bin/pi", Version: "1.5.0"}}}
	m = step(t, m, healthMsg{well})
	assert.Contains(t, screen(m), "2 Health   3 Settings", "nothing to read")
	m, _ = press(t, m, "2")
	shown := screen(m)
	assert.Contains(t, shown, "Checks · checked 1m ago")
	assert.Contains(t, shown, "✓ Token             The profile's credential yields a token")
	assert.Contains(t, shown, "✓ Claude Code       2.1.0 (Claude Code) · used as orchestrator")
	assert.Contains(t, shown, "✓ Codex             codex-cli 0.1 · used as worker")
	assert.Contains(t, shown, "✓ Pi                1.5.0 · used as rule 1")

	// A coding agent the settings use is not installed.
	missing := *well
	missing.Agents = []runner.Installed{{Name: "claude", Path: "/bin/claude", Version: "2.1.0"}, {Name: "codex"}, {Name: "pi", Path: "/bin/pi", Version: "1.5.0"}}
	m = step(t, m, healthMsg{&missing})
	assert.Contains(t, screen(m), "2 Health ✗")
	assert.Contains(t, screen(m), "✗ Codex             Not installed, and the settings use it as worker.")

	// A worker's last turn left something running.
	snap.Done[0].State, snap.Done[0].Process = "failed", 4242
	m = step(t, step(t, m, healthMsg{well}), snapshotMsg{snap})
	assert.Contains(t, screen(m), "2 Health !")
	assert.Contains(t, screen(m), "! Left running      Worker w1 names process group 4242")

	// A hold, and a runner.json that cannot be read.
	snap.Connector.Hold = &connector.HoldStatus{HeldBy: "ann", HeldAt: testNow.Add(-time.Hour)}
	snap.Runner.ConfigError = "rules[0]: unknown adapter"
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "! On hold")
	assert.Contains(t, screen(m), "! Hold              Held since 1h ago by ann")
	assert.Contains(t, screen(m), "✗ runner.json       It cannot be read as written, so no agent is started: rules[0]: unknown adapter")
}

func TestChecksAreAskedAgainOnlyOneAtATime(t *testing.T) {
	asked := 0
	m := open(t, running(), Options{Health: func(context.Context) *Health {
		asked++
		return &Health{At: testNow}
	}}, 110, 30)
	require.True(t, m.checking, "asked as it opens")
	_, cmd := press(t, m, "r")
	assert.Nil(t, cmd, "the first answer is still awaited")

	m = step(t, m, healthMsg{&Health{At: testNow}})
	m, cmd = press(t, m, "r")
	require.NotNil(t, cmd)
	assert.True(t, m.checking)
	m = step(t, m, cmd())
	assert.False(t, m.checking)
	assert.Equal(t, 1, asked)
}

func TestSettingsAreShownAsTheyAreOnDisk(t *testing.T) {
	snap := running()
	yes := true
	snap.Pings = Pings{Hears: &yes, Because: "chosen with --pings"}
	snap.Trust = Trust{Mode: "allowlist", Operator: Person{ID: 7, Name: "Ann"}, Allowlist: []Person{{ID: 8, Name: "Bo"}, {ID: 11}}, Agents: []Person{{ID: 12, Name: "C-3PO"}}}
	m := open(t, snap, Options{}, 110, 50)
	m, _ = press(t, m, "3")
	shown := screen(m)

	assert.Contains(t, shown, "Orchestrator  Claude Code · opus")
	assert.Contains(t, shown, "Worker        Codex")
	assert.Contains(t, shown, "Permissions   full: each coding agent runs with its permission prompts off")
	assert.Contains(t, shown, "At once       no limit: every request gets a worker as it arrives")
	assert.Contains(t, shown, " 1. asked in Launch → Pi")
	assert.Contains(t, shown, "Answers       yes: chosen with --pings")
	assert.Contains(t, shown, "Operator      Ann")
	assert.Contains(t, shown, "Also          Bo, person 11")
	assert.Contains(t, shown, "Assignments   from the operator alone")
	assert.Contains(t, shown, "Other agents  C-3PO, by mention only")
	assert.Contains(t, shown, "Launch · answers 1 chats without a mention")
	assert.NotContains(t, shown, "started without an orchestrator")

	// Assignments are let in from the people named, and only them: being on
	// a served project does not make someone one of them.
	snap.Trust = Trust{Mode: "project", Operator: Person{ID: 7, Name: "Ann"}, Allowlist: []Person{{ID: 8, Name: "Bo"}}, AllowAssignments: true}
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "Also          everyone on a project it serves who is not a client, and Bo")
	assert.Contains(t, screen(m), "Assignments   from the operator and Bo")

	// A cap is the running connector's. One written into runner.json since
	// it started waits for the next start, and is said to.
	snap.Runner.MaxWorkers, snap.Runner.KeptMaxWorkers = 3, 3
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "At once       at most 3 workers: a request past that waits its turn")
	assert.NotContains(t, screen(m), "runner.json now says")
	snap.Runner.KeptMaxWorkers = 1
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, strings.Join(strings.Fields(screen(m)), " "), "runner.json now says one worker at a time: a request past that waits its turn, which applies when the connector next starts")
	snap.Runner.ConfigError, snap.Runner.KeptMaxWorkers = "unexpected end of JSON input", 0
	m = step(t, m, snapshotMsg{snap})
	assert.NotContains(t, screen(m), "runner.json now says", "a file that cannot be read says nothing of its cap")
	assert.Contains(t, screen(m), "cannot be read as written")

	// A connector that hands requests to the session that started it does
	// not use them, and says so.
	snap.Runner.Live = false
	m = step(t, m, snapshotMsg{snap})
	assert.Contains(t, screen(m), "was started without an orchestrator, so it uses none of this")
	m, _ = press(t, m, "1")
	assert.Contains(t, screen(m), "it does none of the work itself")
	assert.Contains(t, strings.Join(strings.Fields(screen(m)), " "), "start it with: basecamp connect -P agent --run")
}

func TestARowOpensInTheBrowserOnlyAtAnAddressOfBasecamps(t *testing.T) {
	var opened []string
	snap := running()
	m := open(t, snap, Options{Open: func(url string) error {
		opened = append(opened, url)
		return nil
	}}, 110, 30)
	m, cmd := press(t, m, "o")
	require.NotNil(t, cmd)
	m = step(t, m, cmd())
	assert.Equal(t, []string{"https://app.basecamp.com/1/buckets/456/card_tables/cards/789"}, opened)

	snap.Now[0].Request.URL = "javascript:alert(1)"
	m = step(t, m, snapshotMsg{snap})
	m, cmd = press(t, m, "o")
	assert.Nil(t, cmd)
	assert.Contains(t, screen(m), "no address in Basecamp to open")
	assert.Len(t, opened, 1)
}

// A command a person is told to run is wrapped at its spaces, never inside
// a flag or a path.
func TestWrappingNeverSplitsAFlagOrAPath(t *testing.T) {
	command := "stop it and start it with: basecamp connect -P rob-agent --run ~/code/my-app"
	for width := 14; width < 80; width++ {
		lines := wrap(command, width)
		assert.Equal(t, command, strings.Join(lines, " "), "width %d", width)
		for _, line := range lines {
			assert.LessOrEqual(t, ansi.StringWidth(line), width)
		}
	}
	assert.Equal(t, []string{"abcd", "efgh", "ij k"}, wrap("abcdefghij k", 4), "a word longer than a line is cut")
	assert.Equal(t, []string{"one", "", "two"}, wrap("one\n\ntwo", 10), "a blank line is kept")
}
