package data

import (
	"context"
	"fmt"
	"sync"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// maxConcurrent limits the number of parallel SDK requests during fan-out
// operations to stay within the shared 50 req/s rate budget.
const maxConcurrent = 5

// AccountInfo represents a discovered Basecamp account.
type AccountInfo struct {
	ID   string
	Name string
}

// AccountResult holds the outcome of a single fan-out query against one account.
type AccountResult struct {
	Account AccountInfo
	Data    any
	Err     error
}

// MultiStore manages cross-account data access. It discovers all accessible
// accounts, caches per-account SDK clients, and provides FanOut for running
// the same query against all accounts concurrently with semaphore limiting.
type MultiStore struct {
	sdk      *basecamp.Client
	mu       sync.RWMutex
	accounts []AccountInfo
	clients  map[string]*basecamp.AccountClient
	identity *basecamp.Identity
}

// NewMultiStore creates a MultiStore backed by the given SDK client.
func NewMultiStore(sdk *basecamp.Client) *MultiStore {
	return &MultiStore{
		sdk:     sdk,
		clients: make(map[string]*basecamp.AccountClient),
	}
}

// DiscoverAccounts fetches all accessible accounts and initializes the store.
// Safe to call multiple times; subsequent calls refresh the account list.
func (ms *MultiStore) DiscoverAccounts(ctx context.Context) ([]AccountInfo, error) {
	info, err := ms.sdk.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{
		FilterProduct: "bc3",
	})
	if err != nil {
		return nil, fmt.Errorf("discovering accounts: %w", err)
	}

	accounts := make([]AccountInfo, 0, len(info.Accounts))
	for _, acct := range info.Accounts {
		if acct.Expired {
			continue
		}
		accounts = append(accounts, AccountInfo{
			ID:   fmt.Sprintf("%d", acct.ID),
			Name: acct.Name,
		})
	}

	ms.mu.Lock()
	ms.accounts = accounts
	ms.identity = &info.Identity
	ms.mu.Unlock()

	return accounts, nil
}

// SetAccountsForTest sets the account list directly, bypassing DiscoverAccounts.
// Only for use in tests.
func (ms *MultiStore) SetAccountsForTest(accounts []AccountInfo) {
	ms.mu.Lock()
	ms.accounts = accounts
	ms.mu.Unlock()
}

// Accounts returns the discovered account list. Returns nil before DiscoverAccounts.
func (ms *MultiStore) Accounts() []AccountInfo {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	cp := make([]AccountInfo, len(ms.accounts))
	copy(cp, ms.accounts)
	return cp
}

// Identity returns the logged-in user's identity, or nil before discovery.
func (ms *MultiStore) Identity() *basecamp.Identity {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if ms.identity == nil {
		return nil
	}
	cp := *ms.identity
	return &cp
}

// ClientFor returns an AccountClient for the given account ID.
// Clients are cached and reused across calls. Returns nil if the
// SDK is not initialized.
func (ms *MultiStore) ClientFor(accountID string) *basecamp.AccountClient {
	if ms.sdk == nil {
		return nil
	}

	ms.mu.RLock()
	if c, ok := ms.clients[accountID]; ok {
		ms.mu.RUnlock()
		return c
	}
	ms.mu.RUnlock()

	c := ms.sdk.ForAccount(accountID)

	ms.mu.Lock()
	ms.clients[accountID] = c
	ms.mu.Unlock()
	return c
}

// FanOut runs fn against all discovered accounts concurrently, limiting
// parallelism to maxConcurrent. Results are returned in account order.
// Individual account failures are captured in AccountResult.Err — the
// overall operation only fails if no accounts are discovered yet.
func (ms *MultiStore) FanOut(ctx context.Context, fn func(acct AccountInfo, client *basecamp.AccountClient) (any, error)) []AccountResult {
	accounts := ms.Accounts()
	if len(accounts) == 0 {
		return nil
	}

	results := make([]AccountResult, len(accounts))
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
				results[idx] = AccountResult{Account: a, Err: ctx.Err()}
				return
			}
			if err := ctx.Err(); err != nil {
				results[idx] = AccountResult{Account: a, Err: err}
				return
			}
			client := ms.ClientFor(a.ID)
			data, err := fn(a, client)
			results[idx] = AccountResult{Account: a, Data: data, Err: err}
		}(i, acct)
	}

	wg.Wait()
	return results
}

// FanOutSingle runs fn against a single account by ID.
// Returns ctx.Err() immediately if the context is already canceled.
func (ms *MultiStore) FanOutSingle(ctx context.Context, accountID string, fn func(ctx context.Context, client *basecamp.AccountClient) (any, error)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client := ms.ClientFor(accountID)
	return fn(ctx, client)
}
