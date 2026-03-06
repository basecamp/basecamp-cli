package views

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// Fixed-width columns for right-aligned tabular display.
const (
	stateColWidth   = 7 // "loading" is the widest state
	poolAgeColWidth = 5 // "999ms" is the widest realistic age
	latColWidth     = 6 // "9999ms" is the widest realistic latency
	feedAgeColWidth = 3 // "59s", "now", "5m"
)

// PoolMonitor is an interactive, focusable view showing pool health and
// activity in a right sidebar. It replaces the former bottom MetricsPanel.
type PoolMonitor struct {
	styles   *tui.Styles
	statsFn  func() []data.PoolStatus
	apdexFn  func() float64
	eventsFn func(int) []data.PoolEvent

	// Pool table (top section)
	poolCursor int
	poolScroll int
	expanded   map[string]bool

	// Activity feed (bottom section)
	feedScroll int

	// Focus
	focused bool
	section int // 0=pool table, 1=activity feed

	width, height int
}

// NewPoolMonitor creates a pool monitor view.
func NewPoolMonitor(
	styles *tui.Styles,
	statsFn func() []data.PoolStatus,
	apdexFn func() float64,
	eventsFn func(int) []data.PoolEvent,
) *PoolMonitor {
	return &PoolMonitor{
		styles:   styles,
		statsFn:  statsFn,
		apdexFn:  apdexFn,
		eventsFn: eventsFn,
		expanded: make(map[string]bool),
	}
}

func (v *PoolMonitor) Title() string { return "Pool Monitor" }

func (v *PoolMonitor) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "expand")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "section")),
	}
}

func (v *PoolMonitor) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

func (v *PoolMonitor) SetSize(w, h int) {
	v.width = w
	v.height = h
}

func (v *PoolMonitor) Init() tea.Cmd { return nil }

func (v *PoolMonitor) Update(msg tea.Msg) (workspace.View, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.FocusMsg:
		v.focused = true
	case workspace.BlurMsg:
		v.focused = false
	case data.PoolUpdatedMsg:
		_ = msg // re-render happens automatically via View()
	case tea.KeyPressMsg:
		if v.focused {
			v.handleKey(msg)
		}
	}
	return v, nil
}

func (v *PoolMonitor) handleKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "j", "down":
		if v.section == 0 {
			stats := v.statsFn()
			if v.poolCursor < len(stats)-1 {
				v.poolCursor++
			}
		} else {
			v.feedScroll++
		}
	case "k", "up":
		if v.section == 0 {
			if v.poolCursor > 0 {
				v.poolCursor--
			}
		} else {
			if v.feedScroll > 0 {
				v.feedScroll--
			}
		}
	case " ":
		if v.section == 0 {
			stats := v.statsFn()
			if v.poolCursor < len(stats) {
				k := stats[v.poolCursor].Key
				v.expanded[k] = !v.expanded[k]
			}
		}
	case "tab":
		v.section = (v.section + 1) % 2
	}
}

func (v *PoolMonitor) View() string {
	if v.width <= 0 || v.height <= 0 {
		return ""
	}

	theme := v.styles.Theme()
	headerStyle := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	successStyle := lipgloss.NewStyle().Foreground(theme.Success)
	errorStyle := lipgloss.NewStyle().Foreground(theme.Error)
	secondaryStyle := lipgloss.NewStyle().Foreground(theme.Secondary)
	primaryStyle := lipgloss.NewStyle().Foreground(theme.Primary)

	// Compute section heights
	tableHeight := v.height * 2 / 5
	if tableHeight < 4 {
		tableHeight = 4
	}
	feedHeight := v.height - tableHeight - 1 // -1 for divider

	// -- Pool table header --
	var lines []string

	apdex := v.apdexFn()
	apdexColor := successStyle
	if apdex < 0.7 {
		apdexColor = errorStyle
	} else if apdex < 0.9 {
		apdexColor = mutedStyle
	}
	header := headerStyle.Render("Pools") + " " + mutedStyle.Render("apdex") + " " + apdexColor.Render(fmt.Sprintf("%.2f", apdex))
	lines = append(lines, ansi.Truncate(header, v.width, ""))

	// -- Pool rows --
	stats := v.statsFn()
	if v.poolCursor >= len(stats) {
		v.poolCursor = max(0, len(stats)-1)
	}

	poolLines := tableHeight - 1 // minus header
	visibleStart := v.poolScroll
	visibleEnd := visibleStart + poolLines
	if visibleEnd > len(stats) {
		visibleEnd = len(stats)
		visibleStart = max(0, visibleEnd-poolLines)
	}
	// Ensure cursor is visible
	if v.poolCursor < visibleStart {
		visibleStart = v.poolCursor
	} else if v.poolCursor >= visibleEnd {
		visibleStart = max(0, v.poolCursor+1-poolLines)
	}
	v.poolScroll = visibleStart

	// suffix column width: [stateCol] [ageCol]
	poolSuffixWidth := stateColWidth + 1 + poolAgeColWidth

	rowCount := 0
	for i := visibleStart; i < len(stats) && rowCount < poolLines; i++ {
		ps := stats[i]
		cursor := " "
		if v.focused && v.section == 0 && i == v.poolCursor {
			cursor = ">"
		}

		// Fetch indicator
		fetchInd := " "
		if ps.Fetching {
			fetchInd = primaryStyle.Render("~")
		}

		// Key (truncated to fit before suffix)
		keyStr := ps.Key
		maxKey := v.width - 2 - poolSuffixWidth - 1 // 2=cursor+fetch, 1=min gap
		if maxKey < 8 {
			maxKey = 8
		}
		if r := []rune(keyStr); len(r) > maxKey {
			keyStr = string(r[:maxKey-1]) + "…"
		}

		// State with color — show "loading" for initial fetches (empty+fetching)
		displayState := ps.State
		if displayState == data.StateEmpty && ps.Fetching {
			displayState = data.StateLoading
		}
		stateStr := displayState.String()
		var stateRendered string
		switch displayState {
		case data.StateError:
			stateRendered = errorStyle.Render(stateStr)
		case data.StateFresh:
			stateRendered = successStyle.Render(stateStr)
		case data.StateStale:
			stateRendered = secondaryStyle.Render(stateStr)
		case data.StateLoading:
			stateRendered = primaryStyle.Render(stateStr)
		default:
			stateRendered = mutedStyle.Render(stateStr)
		}

		// Age — use real disk FetchedAt when cache-seeded for accurate display
		var ageStr string
		ageSrc := ps.FetchedAt
		if !ps.CachedFetchedAt.IsZero() {
			ageSrc = ps.CachedFetchedAt
		}
		if ageSrc.IsZero() {
			ageStr = "-"
		} else {
			ageStr = formatDuration(time.Since(ageSrc))
		}

		// Build row with fixed-width right-aligned columns
		row := cursor + fetchInd + keyStr
		visWidth := lipgloss.Width(row)
		pad := v.width - visWidth - poolSuffixWidth
		if pad < 1 {
			pad = 1
		}
		row += strings.Repeat(" ", pad) +
			rjust(stateRendered, stateColWidth) + " " +
			rjust(mutedStyle.Render(ageStr), poolAgeColWidth)
		lines = append(lines, ansi.Truncate(row, v.width, ""))
		rowCount++

		// Expanded detail line
		if v.expanded[ps.Key] && rowCount < poolLines {
			pollStr := "-"
			if ps.PollInterval > 0 {
				pollStr = formatDuration(ps.PollInterval)
			}
			detail := fmt.Sprintf("  poll:%s h:%d m:%d f:%d e:%d %dms",
				pollStr, ps.HitCount, ps.MissCount, ps.FetchCount, ps.ErrorCount, ps.AvgLatency.Milliseconds())
			lines = append(lines, ansi.Truncate(mutedStyle.Render(detail), v.width, ""))
			rowCount++
		}
	}

	// Pad remaining pool lines
	for rowCount < poolLines {
		lines = append(lines, "")
		rowCount++
	}

	// -- Divider --
	events := v.eventsFn(50)
	divText := fmt.Sprintf("--- Activity (%d) ---", len(events))
	divStyle := mutedStyle
	if v.focused && v.section == 1 {
		divStyle = secondaryStyle
	}
	lines = append(lines, ansi.Truncate(divStyle.Render(divText), v.width, ""))

	// -- Activity feed --
	if feedHeight < 0 {
		feedHeight = 0
	}

	// Clamp feed scroll
	if v.feedScroll > max(0, len(events)-feedHeight) {
		v.feedScroll = max(0, len(events)-feedHeight)
	}

	feedStart := len(events) - feedHeight - v.feedScroll
	if feedStart < 0 {
		feedStart = 0
	}
	feedEnd := feedStart + feedHeight
	if feedEnd > len(events) {
		feedEnd = len(events)
	}

	// suffix column width: [latCol] [ageCol]
	feedSuffixWidth := latColWidth + 1 + feedAgeColWidth

	feedCount := 0
	for i := feedStart; i < feedEnd; i++ {
		ev := events[i]
		keyStr := ev.PoolKey
		maxKey := v.width - 2 - feedSuffixWidth - 1 // 2=indicator+space, 1=min gap
		if maxKey < 8 {
			maxKey = 8
		}
		if r := []rune(keyStr); len(r) > maxKey {
			keyStr = string(r[:maxKey-1]) + "…"
		}

		var line string
		switch ev.EventType {
		case data.FetchComplete:
			latStr := formatDuration(ev.Duration)
			ageStr := formatAge(ev.Timestamp)
			line = successStyle.Render("✓") + " " + keyStr
			pad := v.width - lipgloss.Width(line) - feedSuffixWidth
			if pad < 1 {
				pad = 1
			}
			line += strings.Repeat(" ", pad) +
				rjust(mutedStyle.Render(latStr), latColWidth) + " " +
				rjust(mutedStyle.Render(ageStr), feedAgeColWidth)
		case data.FetchError:
			ageStr := formatAge(ev.Timestamp)
			line = errorStyle.Render("✗") + " " + keyStr
			pad := v.width - lipgloss.Width(line) - feedSuffixWidth
			if pad < 1 {
				pad = 1
			}
			line += strings.Repeat(" ", pad) +
				rjust(errorStyle.Render("err"), latColWidth) + " " +
				rjust(mutedStyle.Render(ageStr), feedAgeColWidth)
		case data.FetchStart:
			line = mutedStyle.Render("~ " + keyStr + " ...")
		}
		lines = append(lines, ansi.Truncate(line, v.width, ""))
		feedCount++
	}

	// Pad remaining feed lines
	for feedCount < feedHeight {
		lines = append(lines, "")
		feedCount++
	}

	return strings.Join(lines, "\n")
}

// rjust right-justifies a (possibly ANSI-styled) string within the given width.
func rjust(s string, width int) string {
	pad := width - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	if d < time.Second {
		return "now"
	}
	return formatDuration(d)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%.0fm", d.Minutes())
}
