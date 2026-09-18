package connector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Retention compares timestamps. Stored as variable-width text, a whole-second
// stamp ("…00Z") sorts after a later fractional one ("…00.5Z") in the same
// second, and a record past its window is kept.
func TestDropContentDropsARecordExpiredWithinTheSameSecond(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	whole := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	ledger.now = func() time.Time { return whole }

	_, err := ledger.RecordSeen(ctx, testEvent(42), LanePoll)
	require.NoError(t, err)
	require.NoError(t, ledger.SetState(ctx, 42, StateDiscarded, "untrusted_author"))

	cutoff := whole.Add(500 * time.Millisecond)
	dropped, err := ledger.DropContent(ctx, cutoff, cutoff)
	require.NoError(t, err)
	assert.Equal(t, 1, dropped, "updated at 12:00:00.000, cutoff 12:00:00.500: expired")
}

// The other direction: a record updated half a second AFTER a whole-second
// cutoff is inside its window and must be kept. As variable-width text,
// "…00.5Z" < "…00Z", so it would be purged.
func TestDropContentKeepsARecordUpdatedJustAfterAWholeSecondCutoff(t *testing.T) {
	ledger := newTestLedger(t)
	ctx := context.Background()
	cutoff := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	ledger.now = func() time.Time { return cutoff.Add(500 * time.Millisecond) }

	_, err := ledger.RecordSeen(ctx, testEvent(43), LanePoll)
	require.NoError(t, err)
	// The lifecycle has no shortcut to completed, so the record walks there.
	dispatchForTest(t, ledger, 43)
	require.NoError(t, ledger.SetState(ctx, 43, StateCompleted, ""))

	dropped, err := ledger.DropContent(ctx, cutoff, cutoff)
	require.NoError(t, err)
	assert.Zero(t, dropped, "updated at 12:00:00.500, cutoff 12:00:00.000: still inside the window")
}

// Every stored timestamp has one width, which is the property both directions
// rest on.
func TestLedgerTimestampsHaveOneWidth(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 16, 12, 0, 0, 500_000_000, time.FixedZone("CEST", 2*3600)),
		time.Date(2026, 9, 16, 12, 0, 0, 123_456_789, time.UTC),
	} {
		s := stamp(at)
		assert.Len(t, s, len("2006-01-02T15:04:05.000000000Z"), s)
		parsed, err := parseStamp(s)
		require.NoError(t, err)
		assert.True(t, parsed.Equal(at))
	}
}
