package data

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seededMultiStore(accounts ...AccountInfo) *MultiStore {
	ms := NewMultiStore(nil)
	ms.mu.Lock()
	ms.accounts = accounts
	ms.mu.Unlock()
	return ms
}

func TestMultiStore_AccountsEmptyBeforeDiscovery(t *testing.T) {
	ms := NewMultiStore(nil)
	assert.Empty(t, ms.Accounts())
	assert.Nil(t, ms.Identity())
}

func TestMultiStore_FanOutNilWithNoAccounts(t *testing.T) {
	ms := NewMultiStore(nil)
	results := ms.FanOut(context.Background(), func(acct AccountInfo, client *basecamp.AccountClient) (any, error) {
		return nil, nil
	})
	assert.Nil(t, results)
}

func TestMultiStore_FanOutCallsEachAccount(t *testing.T) {
	ms := seededMultiStore(
		AccountInfo{ID: "100", Name: "Alpha"},
		AccountInfo{ID: "200", Name: "Beta"},
		AccountInfo{ID: "300", Name: "Gamma"},
	)

	var callCount atomic.Int32
	results := ms.FanOut(context.Background(), func(acct AccountInfo, client *basecamp.AccountClient) (any, error) {
		callCount.Add(1)
		// client is nil because SDK is nil — that's fine for this test
		return "data-for-" + acct.Name, nil
	})

	assert.Equal(t, int32(3), callCount.Load(), "should call fn for each account")
	require.Len(t, results, 3)

	// Results must be in account order regardless of goroutine scheduling
	assert.Equal(t, "Alpha", results[0].Account.Name)
	assert.Equal(t, "Beta", results[1].Account.Name)
	assert.Equal(t, "Gamma", results[2].Account.Name)
	assert.Equal(t, "data-for-Alpha", results[0].Data)
	assert.Equal(t, "data-for-Beta", results[1].Data)
	assert.Equal(t, "data-for-Gamma", results[2].Data)
	assert.Nil(t, results[0].Err)
	assert.Nil(t, results[1].Err)
	assert.Nil(t, results[2].Err)
}

func TestMultiStore_FanOutPartialFailure(t *testing.T) {
	ms := seededMultiStore(
		AccountInfo{ID: "100", Name: "OK1"},
		AccountInfo{ID: "200", Name: "Fail"},
		AccountInfo{ID: "300", Name: "OK2"},
	)

	results := ms.FanOut(context.Background(), func(acct AccountInfo, client *basecamp.AccountClient) (any, error) {
		if acct.Name == "Fail" {
			return nil, fmt.Errorf("simulated error for %s", acct.ID)
		}
		return "success", nil
	})

	require.Len(t, results, 3)

	// Slot 0: OK1 succeeds
	assert.Equal(t, "OK1", results[0].Account.Name)
	assert.Equal(t, "success", results[0].Data)
	assert.Nil(t, results[0].Err)

	// Slot 1: Fail has an error
	assert.Equal(t, "Fail", results[1].Account.Name)
	assert.Nil(t, results[1].Data)
	require.NotNil(t, results[1].Err)
	assert.Contains(t, results[1].Err.Error(), "simulated error")

	// Slot 2: OK2 succeeds
	assert.Equal(t, "OK2", results[2].Account.Name)
	assert.Equal(t, "success", results[2].Data)
	assert.Nil(t, results[2].Err)
}

func TestMultiStore_FanOutConcurrency(t *testing.T) {
	// Seed more accounts than maxConcurrent to verify the semaphore
	// doesn't deadlock and all accounts get processed.
	accounts := make([]AccountInfo, 12)
	for i := range accounts {
		accounts[i] = AccountInfo{ID: fmt.Sprintf("%d", i), Name: fmt.Sprintf("Acct%d", i)}
	}
	ms := seededMultiStore(accounts...)

	var peak atomic.Int32
	var current atomic.Int32

	results := ms.FanOut(context.Background(), func(acct AccountInfo, client *basecamp.AccountClient) (any, error) {
		n := current.Add(1)
		// Track peak concurrency
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		current.Add(-1)
		return acct.ID, nil
	})

	require.Len(t, results, 12, "all 12 accounts should have results")
	assert.LessOrEqual(t, int(peak.Load()), maxConcurrent+1, "peak concurrency should respect semaphore")

	for i, r := range results {
		assert.Equal(t, fmt.Sprintf("%d", i), r.Account.ID, "result slot %d should match account order", i)
		assert.Nil(t, r.Err)
	}
}

func TestMultiStore_FanOutContextCancellation(t *testing.T) {
	accounts := make([]AccountInfo, 10)
	for i := range accounts {
		accounts[i] = AccountInfo{ID: fmt.Sprintf("%d", i), Name: "acct"}
	}
	ms := seededMultiStore(accounts...)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	var executed atomic.Int32
	results := ms.FanOut(ctx, func(acct AccountInfo, client *basecamp.AccountClient) (any, error) {
		executed.Add(1)
		return 1, nil
	})

	assert.Len(t, results, 10)
	for _, r := range results {
		assert.ErrorIs(t, r.Err, context.Canceled)
	}
	assert.Equal(t, int32(0), executed.Load(), "no callbacks should execute when context is pre-canceled")
}

func TestMultiStore_FanOutSingleContextCancellation(t *testing.T) {
	ms := seededMultiStore(AccountInfo{ID: "1", Name: "Alpha"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ms.FanOutSingle(ctx, "1", func(ctx context.Context, client *basecamp.AccountClient) (any, error) {
		return "should not run", nil
	})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMultiStore_AccountsCopyIsolation(t *testing.T) {
	ms := seededMultiStore(AccountInfo{ID: "1", Name: "Orig"})

	accounts := ms.Accounts()
	accounts[0].Name = "Mutated"

	assert.Equal(t, "Orig", ms.Accounts()[0].Name, "store should return isolated copies")
}

func TestMultiStore_IdentityCopyIsolation(t *testing.T) {
	ms := NewMultiStore(nil)

	ms.mu.Lock()
	ms.identity = &basecamp.Identity{FirstName: "Alice", LastName: "Smith"}
	ms.mu.Unlock()

	id := ms.Identity()
	require.NotNil(t, id)
	id.FirstName = "Bob"

	assert.Equal(t, "Alice", ms.Identity().FirstName, "store should return isolated copy")
}

func TestMultiStore_ClientForNilSDK(t *testing.T) {
	ms := NewMultiStore(nil)
	client := ms.ClientFor("123")
	assert.Nil(t, client, "ClientFor should return nil when SDK is nil")
}
