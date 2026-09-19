package commands

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

	// That this read does not wait is setup.TryLock's property and is
	// asserted there (TestTryLockDoesNotWaitForAHolder), where the clock is
	// the thing under test. Here the question is only what it answers.
	projects, release, err := served.Authorize()
	assert.ErrorIs(t, err, connector.ErrPolicyBusy)
	assert.NotErrorIs(t, err, connector.ErrPolicyUnreadable, "somebody holding the lock is not the same as nothing being able to read it")
	assert.Nil(t, projects, "a lock nobody could take is not an empty served set")
	assert.Nil(t, release)
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

	authorized, served, release, err := authorizedProfile(context.Background(), p)
	require.NoError(t, err)
	t.Cleanup(release)
	assert.Equal(t, []int64{48929974}, served, "the unserved project is gone from the set the decision is written against")
	assert.NotContains(t, authorized.file.Projects, int64(48699913), "and gone from the profile everything below decides with")

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

	_, _, _, err = authorizedProfile(context.Background(), connectProfile{name: "agent", path: path, file: file})
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

	_, _, _, err := authorizedProfile(context.Background(), connectProfile{name: "agent", path: path, file: file})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another agent or account")

	unlock, err := setup.TryLock(path)
	require.NoError(t, err, "a refusal leaves no lock behind")
	unlock()
}

// A blocked record's redispatch re-runs what blocked it, and that rerun
// decides with a policy. The policy has to be the one the decision was
// authorized against: `Ledger.Redispatch` sets Rerun for a blocked record
// without consulting the served set at all, so a rerun carrying the file the
// command loaded at start-up can re-admit a record whose project the
// operator unserved in between — and write an admitted verdict that is not
// true of the policy as it stands (Copilot on #771).
func TestARerunDecidesWithThePolicyItWasAuthorizedAgainst(t *testing.T) {
	const withdrawn, kept = int64(48699913), int64(48929974)
	path, startup := policyFile(t, map[int64]admission.Project{kept: {}, withdrawn: {}})

	unserved := startup
	unserved.Projects = map[int64]admission.Project{kept: {}}
	writePolicyFile(t, path, unserved)

	// What the command loaded at start-up still serves the project.
	stale, err := startup.Policy(startup.Agent.PersonID)
	require.NoError(t, err)
	require.Contains(t, stale.Projects, withdrawn, "the stale policy is what would re-admit it")

	authorized, served, release, err := authorizedProfile(context.Background(), connectProfile{name: "agent", path: path, file: startup})
	require.NoError(t, err)
	t.Cleanup(release)

	assert.Equal(t, []int64{kept}, served)
	fresh, err := authorized.file.Policy(authorized.file.Agent.PersonID)
	require.NoError(t, err)
	assert.NotContains(t, fresh.Projects, withdrawn,
		"the rerun decides against the reading the decision was authorized with, not the one the command started with")
	assert.Contains(t, fresh.Projects, kept)
}

// The lock is held across the ledger's write and let go before the caller
// re-runs a prerequisite, on every path out. Both halves are what the shape
// buys: a release left to the command body reads fine and still leaks the
// lock across the network on the path somebody forgets. Asserted against a
// real connect.json and a real ledger rather than against the source, so it
// is the behavior and not the spelling (Codex on #771).
func TestTheRedispatchLetsGoOfTheLockOnEveryPath(t *testing.T) {
	f := newOperatorFixture(t)
	ledger := f.ledger(t, false)
	t.Cleanup(func() { _ = ledger.Close() })
	path := connectSetupPath(t, "agent")
	p := connectProfile{name: "agent", path: path, file: f.file}

	free := func(t *testing.T, why string) {
		t.Helper()
		unlock, err := setup.TryLock(path)
		require.NoError(t, err, why)
		unlock()
	}

	// Record 2 is blocked, so this is the path that writes and then hands
	// back a rerun for the caller to run against Basecamp.
	authorized, res, err := authorizedRedispatch(context.Background(), p, ledger, 2)
	require.NoError(t, err)
	assert.True(t, res.Rerun, "a blocked record's prerequisite runs again")
	assert.NotEmpty(t, authorized.file.Projects, "and the profile comes back carrying the reading it was authorized against")
	free(t, "the lock is let go on the path that succeeds, before the prerequisite is re-run")

	// And on the path that refuses: an event id the ledger has never seen.
	_, _, err = authorizedRedispatch(context.Background(), p, ledger, 9999)
	require.Error(t, err)
	free(t, "and on the path that refuses")
}

// The command reassigns its own profile from the authorized one. Dropping
// that binding is invisible to every other test here and puts the rerun back
// on the file the command loaded at start-up, which is the defect this PR
// was reviewed for (Copilot on #771), so it is pinned as a property of the
// source.
func TestTheRedispatchCommandKeepsNoStartUpReading(t *testing.T) {
	source, err := os.ReadFile("connect_operator.go")
	require.NoError(t, err)
	body := string(source)
	start := strings.Index(body, "func runConnectRedispatch(")
	require.Positive(t, start, "runConnectRedispatch is where the binding lives")
	body = body[start:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}

	call := regexp.MustCompile(`(\w+), \w+, \w+ :?= authorizedRedispatch\(`).FindStringSubmatch(body)
	require.NotNil(t, call, "the command authorizes through authorizedRedispatch")
	assert.Equal(t, "p", call[1],
		"and binds the authorized profile back over p, so the start-up reading is out of scope rather than merely unused")
}
