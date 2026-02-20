package chrome

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Breadcrumb renders a navigable scope trail.
type Breadcrumb struct {
	styles       *tui.Styles
	crumbs       []string
	accountBadge string
	badgeGlobal  bool
	badgeIndex   int // 1-based account index for scoped views, 0 for unindexed
	width        int
}

// NewBreadcrumb creates a new breadcrumb component.
func NewBreadcrumb(styles *tui.Styles) Breadcrumb {
	return Breadcrumb{
		styles: styles,
	}
}

// SetAccountBadge sets the account badge displayed before the breadcrumb trail.
// When global is true, the badge is rendered in a standout color to indicate
// the view aggregates across all accounts.
func (b *Breadcrumb) SetAccountBadge(label string, global bool) {
	b.accountBadge = label
	b.badgeGlobal = global
	b.badgeIndex = 0
}

// SetAccountBadgeIndexed sets a scoped account badge with a numbered index.
// The index is rendered in Foreground and the name in Muted to visually
// connect to the account switcher's numbered shortcuts.
func (b *Breadcrumb) SetAccountBadgeIndexed(index int, name string) {
	b.accountBadge = name
	b.badgeGlobal = false
	b.badgeIndex = index
}

// AccountBadge returns the current badge label (for testing).
func (b *Breadcrumb) AccountBadge() string { return b.accountBadge }

// BadgeGlobal returns whether the badge is in global mode (for testing).
func (b *Breadcrumb) BadgeGlobal() bool { return b.badgeGlobal }

// BadgeIndex returns the badge index (for testing).
func (b *Breadcrumb) BadgeIndex() int { return b.badgeIndex }

// SetCrumbs updates the breadcrumb trail.
func (b *Breadcrumb) SetCrumbs(crumbs []string) {
	b.crumbs = crumbs
}

// SetWidth sets the available width.
func (b *Breadcrumb) SetWidth(w int) {
	b.width = w
}

// Init implements tea.Model.
func (b Breadcrumb) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (b Breadcrumb) Update(msg tea.Msg) (Breadcrumb, tea.Cmd) {
	return b, nil
}

// View renders the breadcrumb trail.
func (b Breadcrumb) View() string {
	if len(b.crumbs) == 0 || b.width <= 0 {
		return ""
	}

	theme := b.styles.Theme()

	var parts []string

	// Account badge
	if b.accountBadge != "" {
		var badge string
		if b.badgeGlobal {
			badge = lipgloss.NewStyle().
				Foreground(theme.Secondary).
				Render("[" + b.accountBadge + "]")
		} else if b.badgeIndex > 0 {
			// Indexed scoped badge: index in Foreground, name in Muted
			idxPart := lipgloss.NewStyle().
				Foreground(theme.Foreground).
				Render(fmt.Sprintf("[%d:", b.badgeIndex))
			namePart := lipgloss.NewStyle().
				Foreground(theme.Muted).
				Render(b.accountBadge + "]")
			badge = idxPart + namePart
		} else {
			badge = lipgloss.NewStyle().
				Foreground(theme.Muted).
				Render("[" + b.accountBadge + "]")
		}
		parts = append(parts, badge)
	}

	for i, crumb := range b.crumbs {
		num := lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render(fmt.Sprintf("%d:", i+1))
		name := lipgloss.NewStyle().
			Foreground(theme.Foreground).
			Bold(i == len(b.crumbs)-1). // last segment is bold
			Render(crumb)
		parts = append(parts, num+name)
	}

	sep := lipgloss.NewStyle().
		Foreground(theme.Border).
		Render(" > ")

	line := strings.Join(parts, sep)

	// Truncate if needed
	if lipgloss.Width(line) > b.width {
		// Show last two segments with ellipsis
		if len(b.crumbs) > 2 {
			ellipsis := lipgloss.NewStyle().Foreground(theme.Muted).Render("... > ")
			last := parts[len(parts)-1]
			line = ellipsis + last
		}
	}

	return lipgloss.NewStyle().Width(b.width).Render(line)
}
