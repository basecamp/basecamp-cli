package workspace

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// The header is three drawn rows — the branded rule, the breadcrumb, the
// separator — plus the row a terminal keeps for itself at the bottom of the
// screen.
const headerHeight = 4

const (
	brandText           = "Basecamp"
	breadcrumbSeparator = " › "

	// The chevron beside the wordmark, pointing the way the menu will go. The
	// full-size triangles rather than the small ones — at one row the small pair
	// reads as a speck.
	chevronClosed = "▼"
	chevronOpen   = "▲"

	// The lead and tail rules on the top line, which never give up their width.
	ruleEnd = 2
)

// renderHeader draws the rows above the content area.
func renderHeader(m *model) string {
	badge, hint := "", ""
	if m.sidebarAvailable() {
		badge, hint = m.sidebar.badge(m.styles), m.styles.Muted.Render(sidebarHintText)
	}

	var b strings.Builder
	b.WriteString(renderTopRule(m.width, m.styles, m.ctx.AccountName(), m.menu.open))
	b.WriteString("\n")
	b.WriteString(renderBreadcrumb(m.width, m.styles, m.nav.trail()))
	b.WriteString("\n")
	b.WriteString(renderDivider(m.width, m.styles, badge, hint))
	return b.String()
}

// renderDivider draws the rule beneath the breadcrumb, with what the frame has to
// say about itself pinned to the right: how much is new in the sidebar, then the
// key that hides it.
//
//	──────────────────── 4 new + ping ── shift+s for sidebar ──
//
// A line too narrow for both gives up the key hint — the reader has seen it
// before, and the count is news.
func renderDivider(width int, styles *tui.Styles, badge, hint string) string {
	if width <= 0 {
		return ""
	}
	rule := lipgloss.NewStyle().Foreground(styles.Theme().Border)
	end := rule.Render(strings.Repeat("─", ruleEnd))

	for _, attempt := range [][]string{{badge, hint}, {badge}, {}} {
		tail := ""
		for _, piece := range attempt {
			if piece != "" {
				tail += " " + piece + " " + end
			}
		}
		if fill := width - lipgloss.Width(tail); fill >= 1 {
			return rule.Render(strings.Repeat("─", fill)) + tail
		}
	}
	return renderRule(width, styles, "")
}

// renderTopRule draws the top line: the account and its menu chevron centered,
// and the key that opens the menu on the right.
//
//	───────────── 37signals ▼ ───────────── ctrl+j for menu ──
//
// The account is what the line is named after, so a line too narrow for both
// shortens the key hint to the key alone and then gives it up entirely — the
// chevron still says there is a menu.
func renderTopRule(width int, styles *tui.Styles, account string, menuOpen bool) string {
	rule := lipgloss.NewStyle().Foreground(styles.Theme().Border)
	label := lipgloss.NewStyle().Foreground(styles.Theme().Border).Bold(true)
	hint := lipgloss.NewStyle().Foreground(styles.Theme().Muted)

	chevron := chevronClosed
	if menuOpen {
		chevron = chevronOpen
	}

	// Until an account has been settled there is no name to show, and the app's
	// own is what the line falls back to.
	name := truncateToWidth(richtext.SanitizeSingleLine(account), max(width/2, 8))
	if name == "" {
		name = brandText
	}
	center := brandStyle(styles).Render(name) + " " + label.Render(chevron)

	for _, attempt := range []string{menuHintText, menuKey, ""} {
		right := ""
		if attempt != "" {
			// Styling an empty string still emits escapes, which would leave
			// the slot's spaces on the line with nothing between them.
			right = hint.Render(attempt)
		}
		if line, ok := ruleWithSlots(width, rule, center, right); ok {
			return line
		}
	}
	return renderRule(width, styles, name)
}

// ruleWithSlots lays a rule out around a centered piece of text and one on the
// right, and reports whether they fit with rule still showing between them.
// Both arrive styled, so they are measured rather than counted.
//
// The centered piece is centered only while there is room for it: past that it
// gives ground to the right-hand slot and drifts left rather than pushing it off
// the line.
func ruleWithSlots(width int, rule lipgloss.Style, center, right string) (string, bool) {
	pad := func(s string) int {
		if s == "" {
			return 0
		}
		return lipgloss.Width(s) + 2
	}
	centerWidth, rightWidth := pad(center), pad(right)
	fill := width - 2*ruleEnd - centerWidth - rightWidth

	before := (width-centerWidth)/2 - ruleEnd
	after := fill - before
	if after < 1 {
		// Centering would push the right-hand slot off the line, so the center
		// gives ground and drifts left instead.
		after = 1
		before = fill - after
	}
	if before < 1 {
		return "", false
	}

	var b strings.Builder
	b.WriteString(rule.Render(strings.Repeat("─", ruleEnd)))
	b.WriteString(rule.Render(strings.Repeat("─", before)))
	b.WriteString(" " + center + " ")
	b.WriteString(rule.Render(strings.Repeat("─", after)))
	if right != "" {
		b.WriteString(" " + right + " ")
	}
	b.WriteString(rule.Render(strings.Repeat("─", ruleEnd)))
	return b.String(), true
}

// brandStyle is the Basecamp yellow, which is the one color that does not come
// from the theme — and drops out entirely when the theme has no colors at all.
func brandStyle(styles *tui.Styles) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if _, colorless := styles.Theme().Primary.(lipgloss.NoColor); colorless {
		return style
	}
	return style.Foreground(tui.BrandColor)
}

// renderRule draws a horizontal rule with a centered label:
//
//	─────────────────── label ───────────────────
func renderRule(width int, styles *tui.Styles, label string) string {
	if width <= 0 {
		return ""
	}
	rule := lipgloss.NewStyle().Foreground(styles.Theme().Border)
	if label == "" || width < 3 {
		return rule.Render(strings.Repeat("─", width))
	}
	padded := " " + truncateToWidth(label, width-2) + " "
	ruleWidth := max(width-lipgloss.Width(padded), 0)
	left := ruleWidth / 2
	return rule.Render(strings.Repeat("─", left) + padded + strings.Repeat("─", ruleWidth-left))
}

// renderBreadcrumb draws the trail of screens the reader walked in through,
// with the one they are on emphasized. It scrolls off the front rather than the
// back when it does not fit: the trail's tail is where they are.
func renderBreadcrumb(width int, styles *tui.Styles, trail []string) string {
	if width <= 0 || len(trail) == 0 {
		return ""
	}
	crumb := lipgloss.NewStyle().Foreground(styles.Theme().Muted)
	here := lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true)
	separator := crumb.Render(breadcrumbSeparator)

	rendered := make([]string, len(trail))
	for index, title := range trail {
		title = richtext.SanitizeSingleLine(title)
		if index == len(trail)-1 {
			rendered[index] = here.Render(title)
		} else {
			rendered[index] = crumb.Render(title)
		}
	}

	for start := range rendered {
		line := strings.Join(rendered[start:], separator)
		if lipgloss.Width(line) <= width {
			if start > 0 {
				line = crumb.Render("…"+breadcrumbSeparator) + line
			}
			return truncateToWidth(line, width)
		}
	}
	return truncateToWidth(rendered[len(rendered)-1], width)
}

// truncateToWidth cuts a rendered string to a cell width, keeping whatever
// escape sequences it already carries intact.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// centerText pads text so it sits in the middle of width.
func centerText(text string, width int) string {
	pad := max((width-lipgloss.Width(text))/2, 0)
	return strings.Repeat(" ", pad) + text
}
