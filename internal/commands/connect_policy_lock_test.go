package commands

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/output"
)

// policyFile is a connect.json on disk where setup and the connector both
// expect one: under the profile's own directory, owner-only, with the
// directories above it owner-only too. Nothing here fakes the lock or the
// read — the tests below take the real flock on the real file.
func policyFile(t *testing.T, projects map[int64]admission.Project) (string, setup.File) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := setup.Path(config.GlobalConfigDir(), "agent")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))

	file := setup.New("agent")
	file.AccountID = "2914079"
	file.Agent = setup.Agent{PersonID: 52007412, Kind: setup.KindAgent}
	file.Trust.OperatorID = 26909558
	file.Projects = projects
	writePolicyFile(t, path, file)
	return path, file
}

func writePolicyFile(t *testing.T, path string, file setup.File) {
	t.Helper()
	require.NoError(t, file.Validate())
	data, err := json.Marshal(file)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func servingNothing(file setup.File) setup.File {
	out := file
	out.Projects = map[int64]admission.Project{}
	return out
}

// Authorize reads connect.json under the lock rather than reusing what the
// TTL cache has: the clock does not move here, so a cached answer would still
// be inside its TTL and would still name the project the operator has just
// unserved. That reused reading is the whole of the window this closes.
func TestAuthorizeReadsTheFileRatherThanTheCache(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {Class: "internal"}})
	clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))
	served.now = func() time.Time { return clock }

	warm, err := served.Current()
	require.NoError(t, err)
	require.Contains(t, warm, int64(48929974), "the cache holds a reading that serves the project")

	writePolicyFile(t, path, servingNothing(file))

	projects, release, err := served.Authorize()
	require.NoError(t, err)
	t.Cleanup(release)
	assert.Empty(t, projects, "the launch is authorized against the file, not against a reading taken before the unserve")

	// And the cache is not left older than what was just read, so the next
	// caller cannot go back to the answer this one refused.
	current, err := served.Current()
	require.NoError(t, err)
	assert.Empty(t, current)
}

// The lock is really taken and really let go: while Authorize's release is
// held nobody else can lock the profile's policy, and once it is released
// they can. A `connect setup --unserve` is on the far side of exactly this.
func TestAuthorizeHoldsThePolicyLockUntilItIsReleased(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))

	_, release, err := served.Authorize()
	require.NoError(t, err)

	_, err = setup.TryLock(path)
	assert.ErrorIs(t, err, setup.ErrSetupRunning, "a setup cannot change the file while a launch is being authorized against it")

	release()
	unlock, err := setup.TryLock(path)
	require.NoError(t, err, "and it can the moment the launch has committed")
	unlock()
}

// The other direction: a policy somebody else holds authorizes nothing, and
// says so as ErrPolicyBusy rather than as an empty served set — the
// dispatcher gives up its pass, and nothing tells an operator their projects
// stopped being served.
func TestAuthorizeIsBusyWhileSomebodyElseHoldsThePolicy(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))

	unlock, err := setup.TryLock(path)
	require.NoError(t, err)
	t.Cleanup(unlock)

	start := time.Now()
	projects, release, err := served.Authorize()
	assert.ErrorIs(t, err, connector.ErrPolicyBusy)
	assert.NotErrorIs(t, err, connector.ErrPolicyUnreadable, "somebody holding the lock is not the same as nothing being able to read it")
	assert.Nil(t, projects, "a lock nobody could take is not an empty served set")
	assert.Nil(t, release)
	assert.Less(t, time.Since(start), setup.LockWait/2, "the dispatcher's read does not wait on a file another process writes")
}

// A connect.json that no longer loads is the other kind of nothing: it
// authorizes none either, and says which it is rather than reporting an
// empty served set or a busy lock.
func TestAuthorizeReportsAPolicyThatCannotBeReadAsUnreadable(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	projects, release, err := served.Authorize()
	assert.ErrorIs(t, err, connector.ErrPolicyUnreadable)
	assert.NotErrorIs(t, err, connector.ErrPolicyBusy)
	assert.Nil(t, projects)
	assert.Nil(t, release)

	unlock, err := setup.TryLock(path)
	require.NoError(t, err, "and the lock it took is not left behind")
	unlock()
}

// connectServedTTL is the width of the one disagreement the lock does not
// close: admission decides against a cached reading, dispatch against a
// locked one, so a project unserved within the TTL can still be admitted —
// and is then refused at the launch and named by the stranded report.
// Raising this constant widens that, and a bound only a comment knows is a
// description rather than a guarantee.
func TestConnectServedTTLStaysSmall(t *testing.T) {
	assert.Positive(t, connectServedTTL, "a reading reused for no time at all reads connect.json once per event")
	assert.LessOrEqual(t, connectServedTTL, 2*time.Second,
		"connectServedTTL is how stale admission's view of the served set may be; raising it is a decision, not an edit")
}

// And the constant is the bound because the cache is real: a reading inside
// the TTL is reused, so the number is what an admission decision can be
// behind the file. Asserting the constant without this would pin a number
// nothing obeys.
func TestAReadingIsReusedWithinItsTTL(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	served := newConnectServed(path, file, slog.New(slog.DiscardHandler))
	served.now = func() time.Time { return clock }

	first, err := served.Current()
	require.NoError(t, err)
	require.Contains(t, first, int64(48929974))

	writePolicyFile(t, path, servingNothing(file))
	clock = clock.Add(connectServedTTL - time.Nanosecond)
	within, err := served.Current()
	require.NoError(t, err)
	assert.Contains(t, within, int64(48929974), "inside the TTL the reading is reused, which is what the TTL bounds")

	clock = clock.Add(time.Nanosecond)
	after, err := served.Current()
	require.NoError(t, err)
	assert.Empty(t, after, "and at the TTL it is read again")
}

// `connect redispatch` authorizes against connect.json as it is when it
// writes, not as it was when the command started. The profile it loaded at
// start-up still serves the project; the file no longer does.
func TestRedispatchReadsTheServedSetUnderTheLock(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}, 48699913: {}})
	p := connectProfile{name: "agent", path: path, file: file}

	stale := servingNothing(file)
	stale.Projects = map[int64]admission.Project{48929974: {}}
	writePolicyFile(t, path, stale)

	served, release, err := servedBucketsUnderLock(p)
	require.NoError(t, err)
	t.Cleanup(release)
	assert.Equal(t, []int64{48929974}, served, "the unserved project is gone from the set the decision is written against")

	_, err = setup.TryLock(path)
	assert.ErrorIs(t, err, setup.ErrSetupRunning, "and the lock is held until the decision has been written")
}

// A setup that outlasts the wait leaves the redispatch unwritten, reported
// as busy and retryable rather than as a policy that refuses the project.
func TestRedispatchIsBusyWhileSetupHoldsThePolicy(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	was := setup.LockWait
	setup.LockWait = 20 * time.Millisecond
	t.Cleanup(func() { setup.LockWait = was })

	unlock, err := setup.TryLock(path)
	require.NoError(t, err)
	t.Cleanup(unlock)

	_, _, err = servedBucketsUnderLock(connectProfile{name: "agent", path: path, file: file})
	require.Error(t, err)
	var apiErr *output.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, output.CodeBusy, apiErr.Code)
	assert.True(t, apiErr.Retryable, "machine consumers read retryable, not the code")
}

// A file that has been pointed at another agent since the command started is
// not this profile's policy, and a decision taken against it would be taken
// for somebody else.
func TestRedispatchRefusesAPolicyThatNowNamesAnotherAgent(t *testing.T) {
	path, file := policyFile(t, map[int64]admission.Project{48929974: {}})
	other := file
	other.Agent.PersonID = 1
	writePolicyFile(t, path, other)

	_, _, err := servedBucketsUnderLock(connectProfile{name: "agent", path: path, file: file})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another agent or account")

	unlock, err := setup.TryLock(path)
	require.NoError(t, err, "a refusal leaves no lock behind")
	unlock()
}
