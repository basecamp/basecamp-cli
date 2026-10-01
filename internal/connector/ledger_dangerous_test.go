package connector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only unfinished requests count, and a request is the person admission
// trusted at the gate's: the performer, else the creator. A record only
// seen is judged again under the trust the connector runs with now.
func TestUnfinishedFromOthersCountsWhatOthersAskedForAndIsStillWaiting(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	const operator, other = int64(111), int64(222)

	addWhy := func(id, creator int64, performer *int64, state RecordState, reason string) {
		t.Helper()
		ev := testEvent(id)
		ev.CreatorID = creator
		ev.PerformedByID = performer
		_, err := ledger.RecordSeen(ctx, ev, LaneLive)
		require.NoError(t, err)
		_, err = ledger.db.ExecContext(ctx, `UPDATE events SET state = ?, reason = ? WHERE id = ?`, string(state), reason, id)
		require.NoError(t, err)
	}
	add := func(id, creator int64, performer *int64, state RecordState) {
		t.Helper()
		addWhy(id, creator, performer, state, "")
	}
	count := func() int {
		t.Helper()
		n, err := ledger.UnfinishedFromOthers(ctx, operator)
		require.NoError(t, err)
		return n
	}

	add(1, operator, nil, StateQueued)
	add(2, other, nil, StateCompleted)
	add(3, other, nil, StateDiscarded)
	add(4, other, nil, StateSeen)
	op := operator
	add(5, other, &op, StateQueued) // the operator assigned someone else's to-do
	assert.Equal(t, 0, count())

	add(6, other, nil, StateQueued)
	assert.Equal(t, 1, count())
	oth := other
	add(7, operator, &oth, StateHeld) // someone else acted on the operator's recording
	assert.Equal(t, 2, count())

	// Blocked in a project the agent doesn't serve: judged again under
	// today's trust, so it never reaches a worker and doesn't count.
	addWhy(8, other, nil, StateBlocked, "no_route")
	assert.Equal(t, 2, count())
	// Blocked after its worker failed to start: a redispatch runs it as it
	// was admitted, so it counts.
	addWhy(9, other, nil, StateBlocked, ReasonSpawnFailed)
	assert.Equal(t, 3, count())
}
