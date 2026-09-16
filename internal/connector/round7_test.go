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
