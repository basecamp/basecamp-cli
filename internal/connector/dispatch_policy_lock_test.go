package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// The served set the launch is decided against is read under connect.json's
// lock and the lock is held until the launch has committed, so an unserve
// lands wholly before the reading or wholly after the task exists. Here it
// lands between the pass's reading and the commit — the window this closes —
// and the task the old order would have started is not started.
//
// The pass still reads the project as served: that reading chooses the
// records worth trying, and choosing a record the launch then refuses is the
// shape this wants, not a disagreement.
func TestALaunchIsDecidedAgainstThePolicyAtTheCommit(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	h.authorized = map[int64]admission.Project{}
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	time.Sleep(150 * time.Millisecond)

	fake.mu.Lock()
	assert.Empty(t, fake.sessions, "an unserve that lands before the commit stops the task it would have started")
	fake.mu.Unlock()
	assert.Equal(t, StateAdmitted, getRecord(t, h.ledger, 1).State, "and the record is left where it was")
}

// A pass that cannot take the policy lock authorizes nothing and loses
// nothing. `connect setup` holds that lock across its network checks, so the
// dispatcher gives up its turn rather than blocking on it; the record is
// still waiting on the next tick.
func TestAPassAuthorizesNothingWhileThePolicyIsBusy(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	h.authorizeErr = ErrPolicyBusy
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	time.Sleep(150 * time.Millisecond)

	fake.mu.Lock()
	require.Empty(t, fake.sessions, "nothing is started while somebody else is changing connect.json")
	fake.mu.Unlock()
	require.Equal(t, StateAdmitted, getRecord(t, h.ledger, 1).State)

	h.mu.Lock()
	h.authorizeErr = nil
	h.mu.Unlock()
	select {
	case <-fake.made:
	case <-time.After(5 * time.Second):
		t.Fatal("the record was not started once the policy was free again")
	}
}

// A reading that cannot be taken at all — a host that cannot lock, a
// connect.json that no longer loads — authorizes nothing either, and is kept
// apart from busy: one says somebody is changing the file, the other says
// something is wrong. Failing closed is the same posture the reader takes,
// and setup takes, when the answer cannot be read.
func TestAPassAuthorizesNothingWhenThePolicyCannotBeRead(t *testing.T) {
	for name, cause := range map[string]error{
		"the policy could not be read under its lock": ErrPolicyUnreadable,
		"an otherwise unclassified error":             errors.New("connect.json cannot be locked"),
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeDriver()
			h := newDispatchHarness(t, fake, nil)
			h.authorizeErr = cause
			admitOn(t, h.ledger, 1, "recording:1")
			h.run(t)
			time.Sleep(150 * time.Millisecond)

			fake.mu.Lock()
			assert.Empty(t, fake.sessions)
			fake.mu.Unlock()
			assert.Equal(t, StateAdmitted, getRecord(t, h.ledger, 1).State)
		})
	}
}

// What is held under the policy lock is one file read and one ledger
// transaction: it is let go when the launch has committed, before a session
// directory, a token socket or a worker process exists. A dispatcher that
// held it across a spawn would park `connect setup` behind a worker.
func TestThePolicyLockIsLetGoBeforeAWorkerStarts(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	held := make(chan bool, 4)
	fake.onStart = func(driver.SessionConfig) {
		h.mu.Lock()
		defer h.mu.Unlock()
		held <- h.policyHeld
	}
	h = newDispatchHarness(t, fake, nil)
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)

	select {
	case stillHeld := <-held:
		assert.False(t, stillHeld, "the policy lock is released when the ledger has committed, not when the worker has started")
	case <-time.After(5 * time.Second):
		t.Fatal("no worker started")
	}
}

// The dispatcher needs both readings, and says which is missing: one to
// choose with, one to authorize with. A dispatcher built without the second
// would launch against whatever the first happened to say.
func TestADispatcherNeedsThePolicyUnderItsLock(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, nil)
	opts := h.d.opts
	opts.Authorize = nil
	_, err := NewDispatcher(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "under its lock")
}

// An Authorize that answers with a served set, no error and no lock to hold
// authorizes nothing. This started life as a no-op release "so a wiring
// error would not panic the dispatcher", which is the same bug this PR is
// about wearing a different hat: no lock is held, the launch proceeds
// anyway, and the ledger records it as authorized. On this path a broken
// contract fails closed (Copilot on #771).
func TestAnAuthorizeWithNoLockToHoldLaunchesNothing(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Authorize = func() (map[int64]admission.Project, func(), error) {
			return map[int64]admission.Project{adapterBucketID: {}}, nil, nil
		}
	})
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)
	time.Sleep(150 * time.Millisecond)

	fake.mu.Lock()
	assert.Empty(t, fake.sessions, "a launch that holds no lock is not an authorized launch")
	fake.mu.Unlock()
	assert.Equal(t, StateAdmitted, getRecord(t, h.ledger, 1).State, "and the record is left for a pass that can take the lock")
}

// And it is refused as the kind of nothing it is: nobody could take the
// lock, which is not the same as somebody holding it.
func TestAnAuthorizeWithNoLockToHoldIsUnreadableNotBusy(t *testing.T) {
	fake := newFakeDriver()
	h := newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Authorize = func() (map[int64]admission.Project, func(), error) {
			return map[int64]admission.Project{adapterBucketID: {}}, nil, nil
		}
	})
	_, _, err := h.d.authorizedBuckets()
	assert.ErrorIs(t, err, ErrPolicyUnreadable)
	assert.NotErrorIs(t, err, ErrPolicyBusy)
}

// The lock is held ACROSS the ledger's commit, not merely taken before it.
// Released a line early and every other test here still passes while the
// window is wide open again, so the release itself looks at the ledger: by
// the time it runs, the record this launch was authorized for has already
// moved to dispatched (Copilot's shape, and Codex on the same PR).
func TestThePolicyLockIsHeldAcrossTheLedgerCommit(t *testing.T) {
	fake := newFakeDriver()
	var h *dispatchHarness
	committed := make(chan RecordState, 4)
	h = newDispatchHarness(t, fake, func(o *DispatcherOptions) {
		o.Authorize = func() (map[int64]admission.Project, func(), error) {
			return map[int64]admission.Project{adapterBucketID: {}}, func() {
				record, ok, err := h.ledger.Get(context.Background(), 1)
				require.NoError(t, err)
				require.True(t, ok)
				committed <- record.State
			}, nil
		}
	})
	admitOn(t, h.ledger, 1, "recording:1")
	h.run(t)

	select {
	case state := <-committed:
		assert.Equal(t, StateDispatched, state,
			"the release runs after the launch transaction, so an unserve waiting on the lock cannot land between the reading and the task")
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was launched")
	}
}
