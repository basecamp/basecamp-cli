package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/console"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
)

func consoleOver(t *testing.T, f operatorFixture) *connectConsole {
	t.Helper()
	return &connectConsole{p: connectProfile{name: "agent", path: connectSetupPath(t, "agent"), file: f.file}}
}

// The console reads what status reads: with no connector running and none
// that ever did the work itself, it still shows the profile, who may give
// the agent work, and what the ledger heard.
func TestTheConsoleShowsAProfileWhoseConnectorIsNotRunning(t *testing.T) {
	f := newOperatorFixture(t)
	ledger := f.ledger(t, false)
	view := consoleOver(t, f)
	snap := view.snapshot(t.Context())
	assert.Equal(t, "agent", snap.Profile)
	assert.Equal(t, f.file.AccountID, snap.AccountID)
	assert.Equal(t, f.file.Agent.PersonID, snap.Agent.ID)
	assert.Equal(t, f.file.Trust.OperatorID, snap.Trust.Operator.ID)
	assert.False(t, snap.Connector.Running)
	assert.Nil(t, snap.Runner, "nothing here has done the work itself")
	assert.Empty(t, snap.Notes)
	require.Len(t, snap.Requests, 2)
	assert.Equal(t, int64(2), snap.Requests[0].EventID, "newest first")
	assert.Equal(t, "blocked", snap.Requests[0].State)
	assert.Equal(t, "admitted", snap.Requests[1].State)

	// One a person closed without running is still a request it heard.
	_, err := ledger.Discard(t.Context(), 2, "local:me")
	require.NoError(t, err)
	require.NoError(t, ledger.Close())
	snap = view.snapshot(t.Context())
	require.Len(t, snap.Requests, 2)
	assert.Equal(t, "discarded", snap.Requests[0].State)
	assert.Equal(t, connector.ReasonByOperator, snap.Requests[0].Reason)
}

// A connector's first start under a profile moves the state directory an
// older CLI left. A console that was open before then follows it.
func TestTheConsoleFollowsAStateDirectoryThatMoves(t *testing.T) {
	f := newOperatorFixture(t)
	view := consoleOver(t, f)
	stateHome, err := connectStateHome()
	require.NoError(t, err)
	legacy, err := ensurePrivateChain(stateHome, "basecamp", "connect", connector.LegacyStateDirName(f.file.AccountID, f.file.Agent.PersonID))
	require.NoError(t, err)

	_, dir, problem := view.current()
	require.NoError(t, problem)
	assert.Equal(t, legacy, dir)

	adopted := filepath.Join(filepath.Dir(legacy), connector.StateDirName(f.file.AccountID, f.file.Agent.PersonID, "agent"))
	require.NoError(t, os.Rename(legacy, adopted))
	_, dir, problem = view.current()
	require.NoError(t, problem)
	assert.Equal(t, adopted, dir)
}

// Health is doctor's as doctor run now would find it. A connect.json that
// can no longer be used fails its check: the checks are not made against
// the file as it was when the console opened.
func TestTheConsolesHealthIsCheckedAgainstConnectJSONAsItStands(t *testing.T) {
	f := newOperatorFixture(t)
	view := consoleOver(t, f)
	path := connectSetupPath(t, "agent")

	p, _, problem := view.current()
	require.NoError(t, problem)
	assert.Equal(t, f.file, p.file)

	require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
	p, _, problem = view.current()
	require.Error(t, problem)
	assert.Equal(t, f.file, p.file, "the last reading that could be made")

	checks := view.checks(t.Context())
	require.Len(t, checks, 1)
	assert.Equal(t, "connect.json", checks[0].Name)
	assert.Equal(t, console.StatusFail, checks[0].Status)
	assert.Contains(t, checks[0].Hint, "basecamp connect setup -P agent")

	snap := view.snapshot(t.Context())
	require.NotEmpty(t, snap.Notes)
	assert.Contains(t, snap.Notes[0], "connect.json cannot be read as it now stands")
}

// A worker is kept for its thread, so the one that finished last may be an
// old one: the list is the latest to finish, not the latest to start.
func TestTheConsoleListsTheWorkersThatFinishedLast(t *testing.T) {
	start := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	ended := func(at time.Time) *time.Time { return &at }
	// As the runner lists them: newest started first.
	var workers []runner.WorkerActivity
	for i := connectConsoleRecent + 5; i >= 1; i-- {
		workers = append(workers, runner.WorkerActivity{WorkerView: runner.WorkerView{
			ID: fmt.Sprintf("w%d", i), State: "done", EndedAt: ended(start.Add(time.Duration(i) * time.Minute))}})
	}
	// The oldest was given another request, and finished it after the rest.
	workers[len(workers)-1].EndedAt = ended(start.Add(24 * time.Hour))

	// One long finished still names a process that could not be confirmed
	// ended. It is kept past the list's length: it is what Health warns of.
	stuck := len(workers) - 2
	workers[stuck].State, workers[stuck].Process = "failed", 4242
	workers = append([]runner.WorkerActivity{
		{WorkerView: runner.WorkerView{ID: "w90", State: "running"}},
		{WorkerView: runner.WorkerView{ID: "w91", State: "queued"}},
	}, workers...)

	now, done := connectConsoleWorkers(workers, true, map[string]string{"w90": "Bash"})
	require.Len(t, done, connectConsoleRecent+1)
	assert.Equal(t, "w2", done[len(done)-1].ID)
	assert.Equal(t, 4242, done[len(done)-1].Process)
	assert.Equal(t, "w1", done[0].ID, "the one that finished last is first")
	assert.Equal(t, fmt.Sprintf("w%d", connectConsoleRecent+5), done[1].ID)
	require.Len(t, now, 2)
	assert.Equal(t, "Bash", now[0].Doing)
	assert.Equal(t, "queued", now[1].State)

	// With no connector running them, they wait for the next start.
	now, _ = connectConsoleWorkers(workers, false, nil)
	assert.Equal(t, "interrupted", now[0].State)
	assert.Equal(t, "interrupted", now[1].State)
}
