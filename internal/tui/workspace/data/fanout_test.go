package data

import (
	"context"
	"errors"
	"testing"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFanOutEmpty(t *testing.T) {
	ms := NewMultiStore(nil)
	results := FanOut(context.Background(), ms,
		func(acct AccountInfo, client *basecamp.AccountClient) (int, error) {
			return 1, nil
		})
	assert.Nil(t, results)
}

func TestFanOutTyped(t *testing.T) {
	ms := NewMultiStore(nil)
	ms.mu.Lock()
	ms.accounts = []AccountInfo{
		{ID: "1", Name: "Alpha"},
		{ID: "2", Name: "Beta"},
	}
	ms.mu.Unlock()

	results := FanOut(context.Background(), ms,
		func(acct AccountInfo, client *basecamp.AccountClient) (string, error) {
			return "data-" + acct.ID, nil
		})

	require.Len(t, results, 2)

	// Results are in account order.
	assert.Equal(t, "1", results[0].Account.ID)
	assert.Equal(t, "data-1", results[0].Data)
	assert.NoError(t, results[0].Err)

	assert.Equal(t, "2", results[1].Account.ID)
	assert.Equal(t, "data-2", results[1].Data)
	assert.NoError(t, results[1].Err)
}

func TestFanOutPartialErrors(t *testing.T) {
	ms := NewMultiStore(nil)
	ms.mu.Lock()
	ms.accounts = []AccountInfo{
		{ID: "1", Name: "Good"},
		{ID: "2", Name: "Bad"},
	}
	ms.mu.Unlock()

	results := FanOut(context.Background(), ms,
		func(acct AccountInfo, client *basecamp.AccountClient) (int, error) {
			if acct.ID == "2" {
				return 0, errors.New("fail")
			}
			return 42, nil
		})

	require.Len(t, results, 2)
	assert.Equal(t, 42, results[0].Data)
	assert.NoError(t, results[0].Err)
	assert.EqualError(t, results[1].Err, "fail")
}

func TestFanOutContextCancellation(t *testing.T) {
	ms := NewMultiStore(nil)
	ms.mu.Lock()
	for i := range 10 {
		ms.accounts = append(ms.accounts, AccountInfo{
			ID: string(rune('A' + i)), Name: "acct",
		})
	}
	ms.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	var executed int32
	results := FanOut(ctx, ms,
		func(acct AccountInfo, client *basecamp.AccountClient) (int, error) {
			executed++
			return 1, nil
		})

	assert.Len(t, results, 10)
	for _, r := range results {
		assert.ErrorIs(t, r.Err, context.Canceled)
	}
	assert.Equal(t, int32(0), executed, "no callbacks should execute when context is pre-canceled")
}
