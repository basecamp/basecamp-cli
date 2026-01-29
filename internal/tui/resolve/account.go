package resolve

import (
	"context"
	"fmt"
	"os"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/tui"
)

// defaultLaunchpadBaseURL is the default Launchpad base URL.
const defaultLaunchpadBaseURL = "https://launchpad.37signals.com"

// getLaunchpadBaseURL returns the Launchpad base URL.
// Can be overridden via BCQ_LAUNCHPAD_URL for testing.
func getLaunchpadBaseURL() string {
	if url := os.Getenv("BCQ_LAUNCHPAD_URL"); url != "" {
		return url
	}
	return defaultLaunchpadBaseURL
}

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

// fetchAccounts retrieves the list of available Basecamp accounts.
func (r *Resolver) fetchAccounts(ctx context.Context) ([]basecamp.AuthorizedAccount, error) {
	// Check authentication
	if !r.auth.IsAuthenticated() {
		return nil, output.ErrAuth("Not authenticated. Run: bcq auth login")
	}

	// Determine authorization endpoint based on OAuth type
	var endpoint string
	oauthType := r.auth.GetOAuthType()
	switch oauthType {
	case "bc3":
		endpoint = r.config.BaseURL + "/authorization.json"
	case "launchpad":
		endpoint = getLaunchpadBaseURL() + "/authorization.json"
	case "":
		// Handle authentication via BASECAMP_TOKEN where no OAuth type is stored.
		// Treat as bc3 since BASECAMP_TOKEN implies direct API access.
		endpoint = r.config.BaseURL + "/authorization.json"
	default:
		return nil, output.ErrAuth("Unknown OAuth type: " + oauthType)
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
