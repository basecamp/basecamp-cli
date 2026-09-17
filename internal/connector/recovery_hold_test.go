//go:build unix

package connector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hold and the cutover: a record still being read when the hold is set,
// or when a shadow is promoted, becomes held rather than dispatched, a crash
// anywhere in shadow promote or import leaves the untouched shadow or a held
// ledger, and no restart dispatches a held record.

// awaitHarnessFile waits for a file a connector process writes.
func (h *harness) awaitFile(name string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(h.t, awaitFile(ctx, filepath.Join(h.dir, name)), "waiting for %s", name)
}

func (h *harness) release(name string) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(filepath.Join(h.dir, name), nil, 0o600))
}

// assertNothingDispatched checks no attempt was ever made and no worker ever
// ran, and that each record is held.
func (h *harness) assertNothingDispatched(l *Ledger, held ...int64) {
	t := h.t
	t.Helper()
	assert.Empty(t, harnessAttempts(t, l), "no attempt")
	assert.Zero(t, h.agentStarts(), "no worker process")
	for _, id := range held {
		assert.Equal(t, StateHeld, stateOf(t, l, id), "event %d", id)
	}
	assert.Empty(t, h.connectorPosts(), "nothing posted under the hold")
}

func TestRecoveryARecordReadDuringTheHoldIsHeld(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		for _, crash := range []bool{false, true} {
			name := "the read completes"
			if crash {
				name = "the connector is killed mid-read and restarted"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, d, harnessScenario{ReadGate: []int64{5001}})
				h.publish(feedEntry{Event: todoEvent(101, 5001)})
				run := harnessRun{Until: "state:101=held"}
				if crash {
					run = harnessRun{Until: "never", Killed: true}
				}
				cmd, out := h.start(run)
				h.awaitFile("read-waiting-5001")

				l := h.ledger()
				_, err := l.SetHold(context.Background(), "operator", HoldByOperator)
				require.NoError(t, err)
				assert.Equal(t, StateSeen, stateOf(t, l, 101), "the read is still in flight at the hold")
				if crash {
					require.NoError(t, cmd.Process.Kill())
				} else {
					h.release("read-release-5001")
				}
				h.wait(cmd, out, run)
				if crash {
					h.sc.ReadGate = nil
					h.writeScenario()
				}

				// A supervisor restart, however many times.
				h.run(harnessRun{})
				h.run(harnessRun{})
				h.assertNothingDispatched(l, 101)

				// Released, a held record stays held; a new one runs.
				_, err = l.Release(context.Background(), "operator")
				require.NoError(t, err)
				h.publish(feedEntry{Event: todoEvent(102, 5002)})
				h.run(harnessRun{})
				assert.Equal(t, StateHeld, stateOf(t, l, 101))
				assert.Equal(t, 0, h.handed(101))
				assert.Equal(t, 1, h.handed(102), "the connector does dispatch what the hold does not hold")
				assert.Equal(t, string(OutcomeSucceeded), outcomeOf(t, l, 102))
			})
		}
	})
}

// cutover is a shadow run's state directory and the normal one beside it.
type cutover struct {
	shadowDir, stateDir string
}

// newCutover lays the two directories out under the harness's own state home,
// where the connector puts them, so a worker's MCP server would resolve the
// promoted one exactly as it resolves an ordinary run's.
func newCutover(h *harness) cutover {
	t := h.t
	t.Helper()
	root := filepath.Join(h.dir, "state", "basecamp")
	c := cutover{
		shadowDir: filepath.Join(root, "connect-shadow", StateDirName(harnessAccount, harnessAgent)),
		stateDir:  filepath.Join(root, "connect", StateDirName(harnessAccount, harnessAgent)),
	}
	for _, dir := range []string{root, filepath.Dir(c.shadowDir), filepath.Dir(c.stateDir), c.shadowDir, c.stateDir} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o700))
	}
	return c
}

// shadowLedger runs a shadow connector that admits event 101 and is stopped
// while event 102's read is in flight.
func (h *harness) shadowLedger(c cutover) {
	h.t.Helper()
	h.sc.ReadGate = []int64{5002}
	h.writeScenario()
	h.publish(feedEntry{Event: todoEvent(101, 5001)})
	h.run(harnessRun{Shadow: true, StateDir: c.shadowDir, Until: "state:101=admitted"})

	h.publish(feedEntry{Event: todoEvent(102, 5002)})
	run := harnessRun{Shadow: true, StateDir: c.shadowDir, Until: "never", Killed: true}
	cmd, out := h.start(run)
	h.awaitFile("read-waiting-5002")
	require.NoError(h.t, cmd.Process.Kill())
	h.wait(cmd, out, run)

	h.sc.ReadGate = nil
	h.writeScenario()
}

func (c cutover) normalLedgerExists() bool {
	_, err := os.Lstat(filepath.Join(c.stateDir, LedgerFile))
	return err == nil
}

func TestRecoveryACrashInShadowPromoteNeverDispatchesAHeldRecord(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		for _, step := range []string{"locked", "marker", "tagged", "held", "checkpointed", "renamed", "synced"} {
			t.Run(step, func(t *testing.T) {
				h := newHarness(t, d, harnessScenario{})
				c := newCutover(h)
				h.shadowLedger(c)

				runKilled(t, "promote:"+step, "SHADOW_DIR="+c.shadowDir, "STATE_DIR="+c.stateDir)
				if c.normalLedgerExists() {
					// The supervisor restarts the connector over whatever the
					// crash left at the normal path.
					t.Logf("killed at %q: the ledger is at the normal path, and a restart must dispatch nothing", step)
					h.run(harnessRun{StateDir: c.stateDir})
					h.assertNothingDispatched(h.ledgerAt(c.stateDir), 101, 102)
				} else {
					t.Logf("killed at %q: the shadow is untouched or held, and there is nothing at the normal path to restart over", step)
					assertUntouchedOrHeldShadow(t, c.shadowDir)
				}

				got, err := PromoteShadow(context.Background(), PromoteOptions{
					ShadowDir: c.shadowDir, StateDir: c.stateDir, AccountID: harnessAccount, AgentID: harnessAgent, By: "operator",
				})
				require.NoError(t, err)
				assert.Equal(t, HoldByPromote, got.Hold.Cause)
				h.run(harnessRun{StateDir: c.stateDir})
				h.run(harnessRun{StateDir: c.stateDir})
				l := h.ledgerAt(c.stateDir)
				h.assertNothingDispatched(l, 101, 102)
			})
		}
	})
}

func TestRecoveryACrashInImportNeverDispatchesAHeldRecord(t *testing.T) {
	forEachDriver(t, func(t *testing.T, d harnessDriver) {
		raceSubset(t, false)
		for _, step := range []string{"entry", "tagged"} {
			t.Run(step, func(t *testing.T) {
				h := newHarness(t, d, harnessScenario{})
				c := newCutover(h)
				h.shadowLedger(c)
				_, err := PromoteShadow(context.Background(), PromoteOptions{
					ShadowDir: c.shadowDir, StateDir: c.stateDir, AccountID: harnessAccount, AgentID: harnessAgent, By: "operator",
				})
				require.NoError(t, err)

				file := `{"version":1,"entries":[{"event_id":102,"decision":"done"},{"event_id":101,"decision":"held"}]}`
				runKilled(t, "import:"+step, "LEDGER="+filepath.Join(c.stateDir, LedgerFile), "RECONCILIATION="+file)
				h.run(harnessRun{StateDir: c.stateDir})
				l := h.ledgerAt(c.stateDir)
				h.assertNothingDispatched(l, 101, 102)

				// The import run again applies whole, and still nothing runs.
				r, err := ParseReconciliation([]byte(file))
				require.NoError(t, err)
				_, err = l.Import(context.Background(), r, "operator")
				require.NoError(t, err)
				h.run(harnessRun{StateDir: c.stateDir})
				assert.Empty(t, harnessAttempts(t, l))
				assert.Equal(t, StateHeld, stateOf(t, l, 101))
				assert.Equal(t, StateDiscarded, stateOf(t, l, 102))
			})
		}
	})
}

// assertUntouchedOrHeldShadow is the other half of the promote rule: what the
// crash left is a shadow ledger, held or exactly as it was — never an unheld
// ledger at the normal path, which the caller has already established is not
// there.
func assertUntouchedOrHeldShadow(t *testing.T, shadowDir string) {
	t.Helper()
	l, err := OpenLedgerReadOnly(context.Background(), filepath.Join(shadowDir, LedgerFile))
	require.NoError(t, err, "the shadow ledger is still where it was")
	defer func() { _ = l.Close() }()
	held, err := l.Held(context.Background())
	require.NoError(t, err)
	state := stateOf(t, l, 101)
	if held {
		assert.Equal(t, StateHeld, state, "a held shadow holds its waiting record")
	} else {
		assert.Equal(t, StateAdmitted, state, "an untouched shadow is as the crash found it")
	}
}

func (h *harness) ledgerAt(dir string) *Ledger {
	h.t.Helper()
	l, err := OpenLedger(filepath.Join(dir, LedgerFile))
	require.NoError(h.t, err)
	h.t.Cleanup(func() { _ = l.Close() })
	return l
}
