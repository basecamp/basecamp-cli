package workspace

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func plainStyles(t *testing.T) *tui.Styles {
	t.Helper()
	return tui.NewStylesWithTheme(tui.NoColorTheme())
}

func TestRenderRule(t *testing.T) {
	styles := plainStyles(t)

	assert.Equal(t, strings.Repeat("─", 20), ansi.Strip(renderRule(20, styles, "")))
	assert.Equal(t, "────── label ───────", ansi.Strip(renderRule(20, styles, "label")))
	assert.Equal(t, "", renderRule(0, styles, "label"))
}

// The account names the line: it sits in the middle with the caret beside it,
// and the key that opens the menu goes on the right.
func TestRenderTopRule(t *testing.T) {
	styles := plainStyles(t)

	rule := ansi.Strip(renderTopRule(70, styles, "37signals", false))
	assert.Equal(t, 70, lipgloss.Width(rule))
	assert.Contains(t, rule, " 37signals "+chevronClosed+" ")
	assert.True(t, strings.HasSuffix(rule, " "+menuHintText+" ──"))
	assert.NotContains(t, rule, brandText)
}

// Until an account is settled there is no name to show, so the line falls back
// to the app's own.
func TestRenderTopRuleWithoutAnAccount(t *testing.T) {
	rule := ansi.Strip(renderTopRule(70, plainStyles(t), "", false))

	assert.Equal(t, 70, lipgloss.Width(rule))
	assert.Contains(t, rule, " "+brandText+" "+chevronClosed+" ")
}

// The caret points the way the menu will go, so it turns over while it is up.
func TestRenderTopRuleCaretFollowsTheMenu(t *testing.T) {
	styles := plainStyles(t)

	open := ansi.Strip(renderTopRule(70, styles, "37signals", true))
	assert.Contains(t, open, chevronOpen)
	assert.NotContains(t, open, chevronClosed)

	// The hint stays put — the menu hangs a row below this line, not over it.
	assert.True(t, strings.HasSuffix(open, " "+menuHintText+" ──"))
}

// labelCenter is the display column the middle of "<name> ▼" sits at. It
// measures the prefix rather than counting it, since strings.Index counts bytes
// and the rule is full of multi-byte dashes.
func labelCenter(t *testing.T, rule, name string) int {
	t.Helper()

	label := name + " " + chevronClosed
	start := strings.Index(rule, label)
	require.GreaterOrEqual(t, start, 0, "no %q in %q", label, rule)
	return lipgloss.Width(rule[:start]) + lipgloss.Width(label)/2
}

// The name is centered while there is room for it on both sides.
func TestRenderTopRuleCentersTheName(t *testing.T) {
	styles := plainStyles(t)

	for _, account := range []string{"37signals", "Basecamp HQ"} {
		rule := ansi.Strip(renderTopRule(90, styles, account, false))
		assert.Equal(t, 90, lipgloss.Width(rule))
		assert.InDelta(t, 45, labelCenter(t, rule, account), 1, "account %q", account)
	}
}

// Past the point where it fits, the name drifts left rather than pushing the
// key hint off the line.
func TestRenderTopRuleDriftsRatherThanDropsTheHint(t *testing.T) {
	rule := ansi.Strip(renderTopRule(44, plainStyles(t), "A Long Account Name", false))

	assert.Equal(t, 44, lipgloss.Width(rule))
	assert.Contains(t, rule, "A Long Account Name")
	assert.Less(t, labelCenter(t, rule, "A Long Account Name"), 22)
}

// A name too wide for half the line is cut rather than dropped: which account
// you are in is what the line is for.
func TestRenderTopRuleTruncatesALongAccountName(t *testing.T) {
	rule := ansi.Strip(renderTopRule(70, plainStyles(t), strings.Repeat("Wide Name ", 8), false))

	assert.Equal(t, 70, lipgloss.Width(rule))
	assert.Contains(t, rule, "Wide Name")
	assert.Contains(t, rule, "…")
	assert.Contains(t, rule, chevronClosed)
}

// A line too narrow for both shortens the key hint to the key alone, then gives
// it up entirely — the caret still says there is a menu.
func TestRenderTopRuleGivesUpInOrder(t *testing.T) {
	styles := plainStyles(t)

	tight := ansi.Strip(renderTopRule(30, styles, "37signals", false))
	assert.Equal(t, 30, lipgloss.Width(tight))
	assert.Contains(t, tight, "37signals "+chevronClosed)
	assert.NotContains(t, tight, menuHintText)
	assert.Contains(t, tight, menuKey)

	tighter := ansi.Strip(renderTopRule(20, styles, "37signals", false))
	assert.Equal(t, 20, lipgloss.Width(tighter))
	assert.Contains(t, tighter, "37signals "+chevronClosed)
	assert.NotContains(t, tighter, menuKey)

	narrowest := ansi.Strip(renderTopRule(10, styles, "37signals", false))
	assert.Equal(t, 10, lipgloss.Width(narrowest))
}

// Whatever it gives up, the line is always exactly the width it was given —
// a rule one column over wraps and pushes the whole screen down a row.
func TestRenderTopRuleAlwaysFillsTheWidth(t *testing.T) {
	styles := plainStyles(t)

	for width := 1; width <= 120; width++ {
		for _, account := range []string{"", "37signals", "A Very Long Account Name Indeed"} {
			rule := renderTopRule(width, styles, account, false)
			assert.Equal(t, width, lipgloss.Width(ansi.Strip(rule)),
				"width %d, account %q", width, account)
		}
	}
}

// An account name is what someone typed into Basecamp, so it must not be able
// to repaint the header.
func TestRenderTopRuleSanitizesTheAccountName(t *testing.T) {
	rule := renderTopRule(70, plainStyles(t), "Ship\x1b[2Jit", false)

	assert.NotContains(t, rule, "\x1b[2J")
	assert.Contains(t, ansi.Strip(rule), "Shipit")
}

func TestRenderBreadcrumb(t *testing.T) {
	styles := plainStyles(t)

	assert.Equal(t, "Home", ansi.Strip(renderBreadcrumb(60, styles, []string{"Home"})))
	assert.Equal(t, "Home › Projects › Website redesign",
		ansi.Strip(renderBreadcrumb(60, styles, []string{"Home", "Projects", "Website redesign"})))
	assert.Equal(t, "", renderBreadcrumb(0, styles, []string{"Home"}))
}

// A trail too long for the screen loses its head, not its tail: where the
// reader is standing is the part they need.
func TestRenderBreadcrumbDropsTheOldestCrumbs(t *testing.T) {
	trail := []string{"Home", "Projects", "Website redesign", "To-dos", "Launch checklist"}

	crumbs := ansi.Strip(renderBreadcrumb(30, plainStyles(t), trail))

	assert.LessOrEqual(t, lipgloss.Width(crumbs), 30)
	assert.Contains(t, crumbs, "Launch checklist")
	assert.True(t, strings.HasPrefix(crumbs, "… › "))
	assert.NotContains(t, crumbs, "Home")
}

// A title carrying control characters is a name someone typed in Basecamp, and
// it must not be able to move the cursor or repaint the header.
func TestRenderBreadcrumbSanitizesTitles(t *testing.T) {
	crumbs := renderBreadcrumb(60, plainStyles(t), []string{"Home", "Ship\x1b[2Jit"})

	assert.NotContains(t, crumbs, "\x1b[2J")
	assert.Contains(t, ansi.Strip(crumbs), "it")
}

func TestTruncateToWidth(t *testing.T) {
	assert.Equal(t, "", truncateToWidth("anything", 0))
	assert.Equal(t, "short", truncateToWidth("short", 10))
	assert.Equal(t, 5, lipgloss.Width(truncateToWidth("a much longer line", 5)))
}

func TestCenterText(t *testing.T) {
	assert.Equal(t, "   abc", centerText("abc", 10))
	assert.Equal(t, "abc", centerText("abc", 2))
}
