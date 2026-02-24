package views

import (
	"fmt"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// sessionAccounts safely returns the discovered accounts from the session's
// MultiStore. Returns nil when session or multiStore is not configured.
func sessionAccounts(session *workspace.Session) []data.AccountInfo {
	if session == nil {
		return nil
	}
	ms := session.MultiStore()
	if ms == nil {
		return nil
	}
	return ms.Accounts()
}

// accountExtra prefixes extra with a 1-based account index when multi-account.
// Returns extra unchanged when len(accounts) <= 1, accountID is not found,
// or extra is empty. Never creates Extra where none existed — the list widget
// truncates Description to fit when Extra is also present.
func accountExtra(accounts []data.AccountInfo, accountID, extra string) string {
	if extra == "" || len(accounts) <= 1 {
		return extra
	}
	idx := accountIndex(accounts, accountID)
	if idx == 0 {
		return extra
	}
	return fmt.Sprintf("%d\u00b7%s", idx, extra)
}

// accountIndex returns the 1-based position of accountID in accounts, or 0.
// Ordering matches MultiStore.Accounts() which is the same ordering used by
// the account switcher and breadcrumb badge.
func accountIndex(accounts []data.AccountInfo, id string) int {
	for i, a := range accounts {
		if a.ID == id {
			return i + 1
		}
	}
	return 0
}
