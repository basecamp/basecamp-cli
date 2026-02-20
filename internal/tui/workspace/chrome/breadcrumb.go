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
}

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
		color := theme.Muted
		if b.badgeGlobal {
			color = theme.Secondary
		}
		badge := lipgloss.NewStyle().
			Foreground(color).
			Render("[" + b.accountBadge + "]")
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
