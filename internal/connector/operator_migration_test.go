//go:build unix

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shadow promote and import (invariant 7), with a real process killed at every
// step.

const (
	opAccount = "2914079"
	opAgent   = adapterAgentID
)

// shadowFixture is a shadow state directory whose ledger holds an admitted
// record (1), a seen record (2), a blocked record (3) and a discarded one (4),
// and the empty normal state directory beside it.
func shadowFixture(t *testing.T) (shadowDir, stateDir string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "basecamp")
	shadowDir = filepath.Join(root, "connect-shadow", StateDirName(opAccount, opAgent))
	stateDir = filepath.Join(root, "connect", StateDirName(opAccount, opAgent))
	for _, dir := range []string{root, filepath.Dir(shadowDir), filepath.Dir(stateDir)} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o700))
	}
	l, err := OpenLedger(filepath.Join(shadowDir, LedgerFile))
	require.NoError(t, err)
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:1")
	seenRecord(t, l, 2)
	seenRecord(t, l, 3)
	_, err = l.Admission().Commit(ctx, blockedVerdict(3, 0, "read_failed"))
	require.NoError(t, err)
	seenRecord(t, l, 4)
	require.NoError(t, l.SetState(ctx, 4, StateDiscarded, "untrusted_author"))
	require.NoError(t, l.Close())
	return shadowDir, stateDir
}

func promoteOptions(shadowDir, stateDir string) PromoteOptions {
	return PromoteOptions{ShadowDir: shadowDir, StateDir: stateDir, AccountID: opAccount, AgentID: opAgent, By: opBy}
}

// Done when: shadow promote yields a held ledger at the normal path, with every
// non-terminal record tagged and the waiting one held.
func TestShadowPromoteYieldsAHeldLedger(t *testing.T) {
	shadowDir, stateDir := shadowFixture(t)
	ctx := context.Background()

	got, err := PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.NoError(t, err)
	assert.Equal(t, HoldByPromote, got.Hold.Cause)
	assert.Equal(t, 3, got.Tagged)
	assert.Equal(t, 1, got.Held)
	_, err = os.Lstat(filepath.Join(shadowDir, LedgerFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "the shadow ledger moved")

	l, err := OpenLedger(filepath.Join(stateDir, LedgerFile))
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	held, err := l.Held(ctx)
	require.NoError(t, err)
	assert.True(t, held)
	assert.Equal(t, StateHeld, stateOf(t, l, 1))
	assert.Equal(t, StateHeld, admitSeen(t, l, 2), "a shadow record mid-read becomes held when admitted")

	again, err := PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.NoError(t, err)
	assert.True(t, again.Already)
}

func admitSeen(t *testing.T, l *Ledger, id int64) RecordState {
	t.Helper()
	record := getRecord(t, l, id)
	v := admittedVerdict(id, record.Revision, "recording:"+strconv.FormatInt(id, 10))
	state, err := l.Admission().Commit(context.Background(), v)
	require.NoError(t, err)
	return RecordState(state)
}

//nolint:contextcheck // subtests build their fixtures on background contexts
func TestShadowPromoteRefusesARunningShadowOrAnExistingLedger(t *testing.T) {
	ctx := context.Background()
	t.Run("the shadow is running", func(t *testing.T) {
		shadowDir, stateDir := shadowFixture(t)
		lock, err := AcquireInstanceLock(shadowDir, opAccount, opAgent, timeNow())
		require.NoError(t, err)
		defer func() { _ = lock.Release() }()
		_, err = PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
		require.ErrorIs(t, err, ErrAlreadyRunning)
		assertUntouchedShadow(t, shadowDir)
	})
	t.Run("the connector is running", func(t *testing.T) {
		shadowDir, stateDir := shadowFixture(t)
		lock, err := AcquireInstanceLock(stateDir, opAccount, opAgent, timeNow())
		require.NoError(t, err)
		defer func() { _ = lock.Release() }()
		_, err = PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
		require.ErrorIs(t, err, ErrAlreadyRunning)
		assertUntouchedShadow(t, shadowDir)
	})
	t.Run("a ledger is already there", func(t *testing.T) {
		shadowDir, stateDir := shadowFixture(t)
		l, err := OpenLedger(filepath.Join(stateDir, LedgerFile))
		require.NoError(t, err)
		require.NoError(t, l.Close())
		_, err = PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
		require.ErrorIs(t, err, ErrLedgerExists)
		assertUntouchedShadow(t, shadowDir)
	})
}

// assertUntouchedShadow checks the shadow ledger is where it was, unheld, its
// records as the fixture left them.
func assertUntouchedShadow(t *testing.T, shadowDir string) {
	t.Helper()
	l, err := OpenLedgerReadOnly(context.Background(), filepath.Join(shadowDir, LedgerFile))
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	held, err := l.Held(context.Background())
	require.NoError(t, err)
	assert.False(t, held)
	assert.Equal(t, StateAdmitted, stateOf(t, l, 1))
}

// crashEnv names the step a helper process kills itself at.
const crashEnv = "BASECAMP_CONNECTOR_CRASH_AT"

// TestCrashHelper is not a test: it is the process the crash tests start and
// kill. It runs promote or import against the directories in its environment
// and SIGKILLs itself at the named step.
func TestCrashHelper(t *testing.T) {
	at := os.Getenv(crashEnv)
	if at == "" {
		t.Skip("run by the crash tests")
	}
	op, step, _ := strings.Cut(at, ":")
	kill := func(name string) {
		if name == step {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	promoteStep, holdStep, importStep = kill, kill, kill
	ctx := context.Background()
	switch op {
	case "promote":
		_, err := PromoteShadow(ctx, promoteOptions(os.Getenv("SHADOW_DIR"), os.Getenv("STATE_DIR")))
		require.NoError(t, err)
	case "import":
		l, err := OpenLedger(os.Getenv("LEDGER"))
		require.NoError(t, err)
		var r Reconciliation
		require.NoError(t, json.Unmarshal([]byte(os.Getenv("RECONCILIATION")), &r))
		_, err = l.Import(ctx, r, opBy)
		require.NoError(t, err)
	}
	t.Fatal("the helper reached its end without being killed at " + step)
}

func runKilled(t *testing.T, at string, env ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestCrashHelper$", "-test.count=1")
	cmd.Env = append(append(os.Environ(), crashEnv+"="+at), env...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "the helper must die: %v\n%s", err, out)
	status, ok := exit.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.True(t, status.Signaled() && status.Signal() == syscall.SIGKILL, "killed at %s, got %v\n%s", at, err, out)
}

// Invariant 7: a crash at any point of promote leaves either the untouched
// shadow or a held ledger — never an unheld ledger at the normal path, never
// two ledgers and never none — and promote run again finishes.
//
//nolint:contextcheck // subtests build their fixtures on background contexts
func TestInvariant7PromoteSurvivesAKillAtEveryStep(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	type crash struct {
		step string
		// preHeld is a shadow already run with --hold, whose marker keeps
		// that cause through the promote.
		preHeld bool
	}
	steps := []string{"locked", "marker", "tagged", "held", "checkpointed", "renamed", "synced"}
	crashes := make([]crash, 0, len(steps)+2)
	crashes = append(crashes, crash{step: "renamed", preHeld: true}, crash{step: "synced", preHeld: true})
	for _, step := range steps {
		crashes = append(crashes, crash{step: step})
	}
	for _, c := range crashes {
		step := c.step
		name := step
		if c.preHeld {
			name += " of a held shadow"
		}
		t.Run(name, func(t *testing.T) {
			shadowDir, stateDir := shadowFixture(t)
			if c.preHeld {
				l, err := OpenLedger(filepath.Join(shadowDir, LedgerFile))
				require.NoError(t, err)
				_, err = l.SetHold(context.Background(), opBy, HoldByOperator)
				require.NoError(t, err)
				require.NoError(t, l.Close())
			}
			runKilled(t, "promote:"+step, "SHADOW_DIR="+shadowDir, "STATE_DIR="+stateDir)

			shadowLedger := filepath.Join(shadowDir, LedgerFile)
			stateLedger := filepath.Join(stateDir, LedgerFile)
			_, shadowErr := os.Lstat(shadowLedger)
			_, stateErr := os.Lstat(stateLedger)
			require.True(t, (shadowErr == nil) != (stateErr == nil), "exactly one ledger exists (shadow: %v, normal: %v)", shadowErr, stateErr)

			if stateErr == nil {
				assertHeld(t, stateLedger)
			} else if c.preHeld || ledgerIsHeld(t, shadowLedger) {
				assertHeld(t, shadowLedger)
			} else {
				assertUntouchedShadow(t, shadowDir)
			}

			got, err := PromoteShadow(context.Background(), promoteOptions(shadowDir, stateDir))
			require.NoError(t, err, "promote run again finishes")
			if !c.preHeld {
				assert.Equal(t, HoldByPromote, got.Hold.Cause)
			}
			assertHeld(t, stateLedger)
		})
	}
}

func ledgerIsHeld(t *testing.T, path string) bool {
	t.Helper()
	l, err := OpenLedgerReadOnly(context.Background(), path)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	held, err := l.Held(context.Background())
	require.NoError(t, err)
	return held
}

func assertHeld(t *testing.T, path string) {
	t.Helper()
	l, err := OpenLedger(path)
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	held, err := l.Held(context.Background())
	require.NoError(t, err)
	require.True(t, held, "%s is held", path)
	assert.Equal(t, StateHeld, stateOf(t, l, 1), "the waiting record is held")
	var untagged int
	require.NoError(t, l.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE state NOT IN ('completed', 'discarded') AND review = 0`).Scan(&untagged))
	assert.Zero(t, untagged, "every non-terminal record is tagged")
}

// Done when: import applies a reconciliation in one transaction — tombstones
// only for done entries, everything else tagged, states and reasons kept.
func TestImportTombstonesDoneAndTagsTheRest(t *testing.T) {
	l := newTestLedger(t)
	ctx := context.Background()
	opAdmit(t, l, 1, "recording:1")
	seenRecord(t, l, 2)
	seenRecord(t, l, 3)
	_, err := l.Admission().Commit(ctx, blockedVerdict(3, 0, "read_failed"))
	require.NoError(t, err)
	seenRecord(t, l, 5)

	got, err := l.Import(ctx, Reconciliation{Version: 1, Entries: []ReconciliationEntry{
		{EventID: 2, Decision: DecisionDone},
		{EventID: 99, Decision: DecisionDone},
		{EventID: 3, Decision: DecisionHeld},
	}}, opBy)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Tombstoned)
	assert.Equal(t, 1, got.Inserted)
	assert.Equal(t, 3, got.Tagged)
	assert.Equal(t, 1, got.Held)

	assert.Equal(t, StateHeld, stateOf(t, l, 1))
	two := getRecord(t, l, 2)
	assert.Equal(t, StateDiscarded, two.State)
	assert.Equal(t, ReasonImportedDone, two.Reason)
	three := getRecord(t, l, 3)
	assert.Equal(t, StateBlocked, three.State)
	assert.Equal(t, "read_failed", three.Reason, "the blocking reason is kept")
	assert.Equal(t, StateSeen, stateOf(t, l, 5))

	fresh, err := l.RecordSeen(ctx, testEvent(99), LanePoll)
	require.NoError(t, err)
	assert.False(t, fresh, "an imported tombstone is never new work")
	assert.Equal(t, StateHeld, admitSeen(t, l, 5), "an unmapped record is tagged too")
}

//nolint:contextcheck // subtests build their fixtures on background contexts
func TestImportRefusesAFileItCannotApplyWhole(t *testing.T) {
	ctx := context.Background()
	for name, entries := range map[string][]ReconciliationEntry{
		"held for an unseen event":    {{EventID: 2, Decision: DecisionDone}, {EventID: 404, Decision: DecisionHeld}},
		"done for a dispatched event": {{EventID: 2, Decision: DecisionDone}, {EventID: 1, Decision: DecisionDone}},
	} {
		t.Run(name, func(t *testing.T) {
			l := newTestLedger(t)
			opAdmit(t, l, 1, "recording:1")
			launchOf(t, l, 1)
			seenRecord(t, l, 2)

			_, err := l.Import(ctx, Reconciliation{Version: 1, Entries: entries}, opBy)
			require.ErrorIs(t, err, ErrDecisionRefused)
			assert.Equal(t, StateSeen, stateOf(t, l, 2), "nothing was applied")
			var tagged int
			require.NoError(t, l.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE review = 1`).Scan(&tagged))
			assert.Zero(t, tagged)
		})
	}
}

func TestParseReconciliationIsStrict(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":    `{"version":1,"entries":[{"event_id":1,"decision":"done","note":"x"}]}`,
		"other decision":   `{"version":1,"entries":[{"event_id":1,"decision":"maybe"}]}`,
		"duplicate":        `{"version":1,"entries":[{"event_id":1,"decision":"done"},{"event_id":1,"decision":"held"}]}`,
		"no id":            `{"version":1,"entries":[{"decision":"done"}]}`,
		"other version":    `{"version":2,"entries":[]}`,
		"trailing value":   `{"version":1,"entries":[]} {}`,
		"trailing brace":   `{"version":1,"entries":[]} }`,
		"trailing bracket": `{"version":1,"entries":[]} ]`,
		"trailing text":    `{"version":1,"entries":[]} done`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseReconciliation([]byte(body))
			assert.Error(t, err)
		})
	}
	r, err := ParseReconciliation([]byte(`{"version":1,"entries":[{"event_id":7,"decision":"held"}]}`))
	require.NoError(t, err)
	assert.Equal(t, []ReconciliationEntry{{EventID: 7, Decision: DecisionHeld}}, r.Entries)
}

// Invariant 7: an import killed mid-transaction applied nothing.
//
//nolint:contextcheck // subtests build their fixtures on background contexts
func TestInvariant7ImportSurvivesAKillAtEveryStep(t *testing.T) {
	if testing.Short() {
		t.Skip("starts processes")
	}
	for _, step := range []string{"entry", "tagged"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", LedgerFile)
			l, err := OpenLedger(path)
			require.NoError(t, err)
			opAdmit(t, l, 1, "recording:1")
			seenRecord(t, l, 2)
			require.NoError(t, l.Close())
			file := `{"version":1,"entries":[{"event_id":2,"decision":"done"},{"event_id":1,"decision":"held"}]}`

			runKilled(t, "import:"+step, "LEDGER="+path, "RECONCILIATION="+file)

			l, err = OpenLedger(path)
			require.NoError(t, err)
			defer func() { _ = l.Close() }()
			assert.Equal(t, StateAdmitted, stateOf(t, l, 1))
			assert.Equal(t, StateSeen, stateOf(t, l, 2))
			var decisions int
			require.NoError(t, l.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM decisions`).Scan(&decisions))
			assert.Zero(t, decisions, fmt.Sprintf("killed at %s: nothing recorded", step))
		})
	}
}

func timeNow() time.Time { return time.Now() }

// A promote run again after its rename finishes the move rather than refusing
// it, and does not mind a shadow directory a person has cleared away.
func TestPromoteRunAgainFinishesAMoveWithoutItsShadow(t *testing.T) {
	shadowDir, stateDir := shadowFixture(t)
	ctx := context.Background()
	_, err := PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(shadowDir))

	got, err := PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.NoError(t, err)
	assert.True(t, got.Already)
	assertHeld(t, filepath.Join(stateDir, LedgerFile))
}

// Import validates the reconciliation it is handed, not only the file it was
// parsed from: a caller that built the value itself meets the same rules.
//
//nolint:contextcheck // subtests build their fixtures on background contexts
func TestImportValidatesWhatItIsHanded(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]Reconciliation{
		"another version":  {Version: ReconciliationVersion + 1},
		"unknown decision": {Version: ReconciliationVersion, Entries: []ReconciliationEntry{{EventID: 1, Decision: "maybe"}}},
		"no event":         {Version: ReconciliationVersion, Entries: []ReconciliationEntry{{Decision: DecisionDone}}},
		"one event twice":  {Version: ReconciliationVersion, Entries: []ReconciliationEntry{{EventID: 1, Decision: DecisionDone}, {EventID: 1, Decision: DecisionHeld}}},
	} {
		t.Run(name, func(t *testing.T) {
			l := newTestLedger(t)
			opAdmit(t, l, 1, "recording:1")

			_, err := l.Import(ctx, r, opBy)
			require.Error(t, err)
			assert.Equal(t, StateAdmitted, stateOf(t, l, 1), "nothing was tagged or closed")
			var tagged int
			require.NoError(t, l.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE review = 1`).Scan(&tagged))
			assert.Zero(t, tagged)
		})
	}
}

// A promote run again takes the connector's lock even when there is no shadow
// state left to stop: it never reports on a ledger a connector is running on.
func TestPromoteRunAgainStillNeedsTheConnectorStopped(t *testing.T) {
	shadowDir, stateDir := shadowFixture(t)
	ctx := context.Background()
	_, err := PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(shadowDir))

	lock, err := AcquireInstanceLock(stateDir, opAccount, opAgent, timeNow())
	require.NoError(t, err)
	_, err = PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.ErrorIs(t, err, ErrAlreadyRunning)
	require.NoError(t, lock.Release())

	got, err := PromoteShadow(ctx, promoteOptions(shadowDir, stateDir))
	require.NoError(t, err)
	assert.True(t, got.Already)
}
