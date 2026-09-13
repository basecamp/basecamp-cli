package output

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/resilience"
)

// A gate rejection reaches the user with its own message and hint through
// the central conversion too, so commands that return SDK errors directly
// render the same thing as those that call convertSDKError.
func TestAsErrorCarriesTheGateMessageAndHint(t *testing.T) {
	store := resilience.NewStore(t.TempDir())
	require.NoError(t, store.Update(func(state *resilience.State) error {
		state.Bulkhead.ActivePIDs = []int{os.Getppid()}
		return nil
	}))
	gateErr := resilience.NewBulkhead(store, resilience.BulkheadConfig{MaxConcurrent: 1}).Wait(context.Background(), time.Now())
	require.Error(t, gateErr)

	err := AsError(fmt.Errorf("listing chat lines: %w", gateErr))

	assert.Equal(t, CodeRateLimit, err.Code)
	assert.Equal(t, "Too many concurrent basecamp processes (limit 1); waited 0s", err.Message)
	assert.Equal(t, "Re-run, or lower parallelism.", err.Hint)
	assert.True(t, err.Retryable)
}
