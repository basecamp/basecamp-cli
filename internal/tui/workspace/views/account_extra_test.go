package views

import (
	"testing"

	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

func TestAccountExtra_SingleAccount(t *testing.T) {
	accounts := []data.AccountInfo{{ID: "1", Name: "Acme"}}

	if got := accountExtra(accounts, "1", "Message"); got != "Message" {
		t.Errorf("single account: got %q, want %q", got, "Message")
	}
	if got := accountExtra(accounts, "1", ""); got != "" {
		t.Errorf("single account empty extra: got %q, want %q", got, "")
	}
}

func TestAccountExtra_MultiAccount(t *testing.T) {
	accounts := []data.AccountInfo{
		{ID: "aaa", Name: "Acme"},
		{ID: "bbb", Name: "Beta"},
		{ID: "ccc", Name: "Gamma"},
	}

	tests := []struct {
		name      string
		accountID string
		extra     string
		want      string
	}{
		{"first account with extra", "aaa", "Message", "1\u00b7Message"},
		{"second account with extra", "bbb", "Todo", "2\u00b7Todo"},
		{"third account with extra", "ccc", "Jan 15", "3\u00b7Jan 15"},
		{"first account empty extra", "aaa", "", "1"},
		{"unknown account", "zzz", "Message", "Message"},
		{"unknown account empty extra", "zzz", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accountExtra(accounts, tt.accountID, tt.extra)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccountExtra_NoAccounts(t *testing.T) {
	if got := accountExtra(nil, "1", "Message"); got != "Message" {
		t.Errorf("nil accounts: got %q, want %q", got, "Message")
	}
	if got := accountExtra([]data.AccountInfo{}, "1", "Message"); got != "Message" {
		t.Errorf("empty accounts: got %q, want %q", got, "Message")
	}
}

func TestAccountIndex(t *testing.T) {
	accounts := []data.AccountInfo{
		{ID: "aaa", Name: "Acme"},
		{ID: "bbb", Name: "Beta"},
	}

	if got := accountIndex(accounts, "aaa"); got != 1 {
		t.Errorf("first account: got %d, want 1", got)
	}
	if got := accountIndex(accounts, "bbb"); got != 2 {
		t.Errorf("second account: got %d, want 2", got)
	}
	if got := accountIndex(accounts, "zzz"); got != 0 {
		t.Errorf("unknown account: got %d, want 0", got)
	}
}
