package resolve

import (
	"context"
	"fmt"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Account resolves the account ID using the following precedence:
// 1. CLI flag (--account)
// 2. Config file (account_id)
// 3. Interactive prompt (if terminal is interactive)
// 4. Error (if no account can be determined)
//
// Returns the resolved account ID and the source it came from.
func (r *Resolver) Account(ctx context.Context) (*ResolvedValue, error) {
	// 1. Check CLI flag
	if r.flags.Account != "" {
		return &ResolvedValue{
			Value:  r.flags.Account,
			Source: SourceFlag,
		}, nil
	}

	// 2. Check config
	if r.config.AccountID != "" {
		return &ResolvedValue{
			Value:  r.config.AccountID,
			Source: SourceConfig,
		}, nil
	}

	// 3. Try interactive prompt if available
	if !r.IsInteractive() {
		return nil, output.ErrUsage("--account is required (or set account_id in config)")
	}

	// Fetch available accounts
	accounts, err := r.fetchAccounts(ctx)
	if err != nil {
		return nil, err
	}

	if len(accounts) == 0 {
		return nil, output.ErrNotFound("account", "any")
	}

	// If only one account, use it automatically
	if len(accounts) == 1 {
		accountID := fmt.Sprintf("%d", accounts[0].ID)
		return &ResolvedValue{
			Value:  accountID,
			Label:  accounts[0].Name,
			Source: SourceDefault,
		}, nil
	}

	// Show picker
	items := make([]tui.PickerItem, len(accounts))
	for i, acct := range accounts {
		items[i] = tui.PickerItem{
			ID:          fmt.Sprintf("%d", acct.ID),
			Title:       acct.Name,
			Description: fmt.Sprintf("#%d", acct.ID),
		}
	}

	selected, err := tui.PickAccount(items)
	if err != nil {
		return nil, fmt.Errorf("account selection failed: %w", err)
	}
	if selected == nil {
		return nil, output.ErrUsage("account selection canceled")
	}

	return &ResolvedValue{
		Value:  selected.ID,
		Label:  selected.Title,
		Source: SourcePrompt,
	}, nil
}

// AccountWithPersist resolves the account ID and optionally prompts to save it.
// This is useful for commands that want to offer to save the selected account.
func (r *Resolver) AccountWithPersist(ctx context.Context) (*ResolvedValue, error) {
	resolved, err := r.Account(ctx)
	if err != nil {
		return nil, err
	}

	// Only prompt to persist if it was selected interactively
	if resolved.Source == SourcePrompt {
		_, _ = PromptAndPersistAccountID(resolved.Value)
	}

	return resolved, nil
}

// ListAccounts returns the list of available Basecamp accounts.
func (r *Resolver) ListAccounts(ctx context.Context) ([]basecamp.AuthorizedAccount, error) {
	return r.fetchAccounts(ctx)
}

// fetchAccounts retrieves the list of available Basecamp accounts.
func (r *Resolver) fetchAccounts(ctx context.Context) ([]basecamp.AuthorizedAccount, error) {
	// Check authentication
	if !r.auth.IsAuthenticated() {
		return nil, output.ErrAuth("Not authenticated. Run: basecamp auth login")
	}

	endpoint, err := r.auth.AuthorizationEndpoint(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch authorization info using SDK
	authInfo, err := r.sdk.Authorization().GetInfo(ctx, &basecamp.GetInfoOptions{
		Endpoint:      endpoint,
		FilterProduct: "bc3",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch accounts: %w", err)
	}

	return authInfo.Accounts, nil
}
