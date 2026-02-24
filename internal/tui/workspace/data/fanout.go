package data

import (
	"context"
	"sync"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// AccountData holds the typed outcome of a fan-out query against one account.
// Replaces the type-erased AccountResult for new pool-based code.
type AccountData[T any] struct {
	Account AccountInfo
	Data    T
	Err     error
}

// FanOut runs fn against all discovered accounts concurrently, limiting
// parallelism to maxConcurrent. Results are returned in account order.
// Unlike MultiStore.FanOut, the result is fully typed — no type assertions.
func FanOut[T any](ctx context.Context, ms *MultiStore,
	fn func(acct AccountInfo, client *basecamp.AccountClient) (T, error),
) []AccountData[T] {
	accounts := ms.Accounts()
	if len(accounts) == 0 {
		return nil
	}

	results := make([]AccountData[T], len(accounts))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for i, acct := range accounts {
		wg.Add(1)
		go func(idx int, a AccountInfo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = AccountData[T]{Account: a, Err: ctx.Err()}
				return
			}
			if err := ctx.Err(); err != nil {
				results[idx] = AccountData[T]{Account: a, Err: err}
				return
			}
			client := ms.ClientFor(a.ID)
			data, err := fn(a, client)
			results[idx] = AccountData[T]{Account: a, Data: data, Err: err}
		}(i, acct)
	}

	wg.Wait()
	return results
}
