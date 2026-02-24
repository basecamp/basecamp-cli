package chrome

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testSwitcher(accounts []AccountEntry) AccountSwitcher {
	s := NewAccountSwitcher(tui.NewStyles())
	s.SetSize(80, 40)
	s.Focus(accounts)
	return s
}

func TestAccountSwitcher_DigitKeySelection(t *testing.T) {
	accounts := []AccountEntry{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}
	s := testSwitcher(accounts)

	// Focus prepends "All Accounts" when >1 account: [All, Acme, Beta]
	// Digit "0" selects "All Accounts"
	cmd := s.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	require.NotNil(t, cmd)
	msg := cmd()
	switched, ok := msg.(AccountSwitchedMsg)
	require.True(t, ok)
	assert.Equal(t, "", switched.AccountID, "0 should select All Accounts")
	assert.Equal(t, "All Accounts", switched.AccountName)
}

func TestAccountSwitcher_DigitKeySelectsAccount(t *testing.T) {
	accounts := []AccountEntry{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}
	s := testSwitcher(accounts)

	// "1" selects first real account (Acme Corp)
	cmd := s.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	require.NotNil(t, cmd)
	msg := cmd()
	switched, ok := msg.(AccountSwitchedMsg)
	require.True(t, ok)
	assert.Equal(t, "1", switched.AccountID)
	assert.Equal(t, "Acme Corp", switched.AccountName)

	// "2" selects second real account (Beta Inc)
	cmd = s.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	require.NotNil(t, cmd)
	msg = cmd()
	switched, ok = msg.(AccountSwitchedMsg)
	require.True(t, ok)
	assert.Equal(t, "2", switched.AccountID)
	assert.Equal(t, "Beta Inc", switched.AccountName)
}

func TestAccountSwitcher_DigitKeyOutOfRange(t *testing.T) {
	accounts := []AccountEntry{
		{ID: "1", Name: "Acme Corp"},
	}
	s := testSwitcher(accounts)

	// Single account: no "All Accounts" prepended, so [Acme].
	// "5" is out of range — should be a no-op.
	cmd := s.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	assert.Nil(t, cmd, "out-of-range digit should be a no-op")
}

func TestAccountSwitcher_NumberedDisplay(t *testing.T) {
	accounts := []AccountEntry{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}
	s := testSwitcher(accounts)

	view := s.View()
	// Verify number prefixes appear in the rendered output
	assert.Contains(t, view, "0", "should show number prefix for All Accounts")
	assert.Contains(t, view, "Acme Corp")
	assert.Contains(t, view, "Beta Inc")
	// Verify footer hint
	assert.Contains(t, view, "0-9/enter select")
}
