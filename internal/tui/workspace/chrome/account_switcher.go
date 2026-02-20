package chrome

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// accountEntry holds a resolved account for display in the switcher.
type accountEntry struct {
	ID   string
	Name string
}

// AccountSwitchedMsg is sent when the user selects an account.
type AccountSwitchedMsg struct {
	AccountID   string
	AccountName string
}

// AccountSwitchCloseMsg is sent when the switcher is dismissed without selecting.
type AccountSwitchCloseMsg struct{}

// accountsLoadedMsg carries async-fetched accounts into the switcher.
type accountsLoadedMsg struct {
	accounts []accountEntry
	err      error
}

// AccountSwitcher is an overlay that lists available Basecamp accounts
// and lets the user pick one. Structurally similar to the command palette.
type AccountSwitcher struct {
	styles *tui.Styles
	sdk    *basecamp.Client

	accounts []accountEntry
	cursor   int
	loading  bool
	err      error

	width, height int
}

// NewAccountSwitcher creates a new account switcher component.
func NewAccountSwitcher(styles *tui.Styles, sdk *basecamp.Client) AccountSwitcher {
	return AccountSwitcher{
		styles: styles,
		sdk:    sdk,
	}
}

// Focus activates the switcher and kicks off an async account fetch.
func (a *AccountSwitcher) Focus() tea.Cmd {
	a.cursor = 0
	a.loading = true
	a.err = nil
	a.accounts = nil

	sdk := a.sdk
	return func() tea.Msg {
		info, err := sdk.Authorization().GetInfo(context.Background(), nil)
		if err != nil {
			return accountsLoadedMsg{err: err}
		}
		var entries []accountEntry
		for _, acct := range info.Accounts {
			entries = append(entries, accountEntry{
				ID:   fmt.Sprintf("%d", acct.ID),
				Name: acct.Name,
			})
		}
		if len(entries) > 1 {
			entries = append([]accountEntry{{ID: "", Name: "All Accounts"}}, entries...)
		}
		return accountsLoadedMsg{accounts: entries}
	}
}

// Blur deactivates the switcher.
func (a *AccountSwitcher) Blur() {
	a.loading = false
	a.err = nil
}

// SetSize sets the available dimensions for the overlay.
func (a *AccountSwitcher) SetSize(width, height int) {
	a.width = width
	a.height = height
}

// Update handles messages for the account switcher.
// Returns a tea.Cmd when the switcher produces an action or wants to close.
func (a *AccountSwitcher) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case accountsLoadedMsg:
		a.loading = false
		if msg.err != nil {
			a.err = msg.err
			return nil
		}
		a.accounts = msg.accounts
		a.cursor = 0
		return nil
	case tea.KeyMsg:
		return a.handleKey(msg)
	}
	return nil
}

func (a *AccountSwitcher) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+a":
		return func() tea.Msg { return AccountSwitchCloseMsg{} }

	case "enter":
		if len(a.accounts) > 0 && a.cursor < len(a.accounts) {
			acct := a.accounts[a.cursor]
			return func() tea.Msg {
				return AccountSwitchedMsg{
					AccountID:   acct.ID,
					AccountName: acct.Name,
				}
			}
		}
		return nil

	case "up", "k":
		if a.cursor > 0 {
			a.cursor--
		}
		return nil

	case "down", "j":
		if a.cursor < len(a.accounts)-1 {
			a.cursor++
		}
		return nil
	}

	// Digit-key selection: 0 selects "All Accounts", 1-9 select accounts.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '0' && r <= '9' {
			idx := int(r - '0')
			if idx < len(a.accounts) {
				acct := a.accounts[idx]
				return func() tea.Msg {
					return AccountSwitchedMsg{
						AccountID:   acct.ID,
						AccountName: acct.Name,
					}
				}
			}
		}
	}
	return nil
}

// maxSwitcherItems is the maximum number of accounts shown at once.
const maxSwitcherItems = 12

// View renders the account switcher overlay.
func (a AccountSwitcher) View() string {
	theme := a.styles.Theme()

	// Box width: 50 chars or terminal width - 8, whichever is smaller
	boxWidth := 50
	if a.width-8 < boxWidth {
		boxWidth = a.width - 8
	}
	if boxWidth < 30 {
		boxWidth = 30
	}

	// Title
	title := lipgloss.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		Render("Switch Account")

	// Separator
	sep := lipgloss.NewStyle().
		Foreground(theme.Border).
		Width(boxWidth - 4).
		Render(strings.Repeat("─", boxWidth-4))

	var rows []string

	if a.loading {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("Loading accounts..."))
	} else if a.err != nil {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(theme.Error).
			Render("Error: "+a.err.Error()))
	} else if len(a.accounts) == 0 {
		rows = append(rows, lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("No accounts found"))
	} else {
		// Scroll window: show maxSwitcherItems around cursor
		start := 0
		if a.cursor >= maxSwitcherItems {
			start = a.cursor - maxSwitcherItems + 1
		}
		end := start + maxSwitcherItems
		if end > len(a.accounts) {
			end = len(a.accounts)
		}
		visible := a.accounts[start:end]
		for vi, acct := range visible {
			i := start + vi
			// Number prefix: "0" for All Accounts, "1"-"9" for real accounts
			numStr := fmt.Sprintf("%d", i)
			numPrefix := lipgloss.NewStyle().Foreground(theme.Muted).Render(numStr + "  ")

			name := lipgloss.NewStyle().Foreground(theme.Primary).Render(acct.Name)
			line := numPrefix + name
			if acct.ID != "" {
				line += lipgloss.NewStyle().Foreground(theme.Muted).Render("  #" + acct.ID)
			}

			if i == a.cursor {
				hlNum := lipgloss.NewStyle().Foreground(theme.Muted).Background(theme.Border).Render(numStr + "  ")
				highlighted := hlNum + lipgloss.NewStyle().Foreground(theme.Primary).Background(theme.Border).Render(acct.Name)
				if acct.ID != "" {
					highlighted += lipgloss.NewStyle().Foreground(theme.Muted).Background(theme.Border).Render("  #" + acct.ID)
				}
				line = lipgloss.NewStyle().
					Background(theme.Border).
					Width(boxWidth - 4).
					Render(highlighted)
			}
			rows = append(rows, line)
		}
	}

	// Footer hint
	footer := lipgloss.NewStyle().Foreground(theme.Muted).Render("0-9/enter select  esc cancel")

	// Assemble
	sections := make([]string, 0, 2+len(rows)+2)
	sections = append(sections, title)
	sections = append(sections, sep)
	sections = append(sections, rows...)
	sections = append(sections, sep)
	sections = append(sections, footer)

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Primary).
		Padding(0, 1).
		Width(boxWidth)

	rendered := box.Render(content)

	// Center horizontally
	return lipgloss.NewStyle().
		Width(a.width).
		Align(lipgloss.Center).
		Render(rendered)
}
