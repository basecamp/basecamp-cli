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
)

// PoolMonitor is an interactive, focusable view showing pool health and
// activity in a right sidebar. The bottom section shows a global activity
// feed that is independent of the pool list cursor position.
type PoolMonitor struct {
	styles   *tui.Styles
	statsFn  func() []data.PoolStatus
	apdexFn  func() float64
	eventsFn func(int) []data.PoolEvent

	// Pool table
	poolCursor int
	poolScroll int
	expanded   map[string]bool

	// Focus
	focused bool

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
		stats := v.statsFn()
		if v.poolCursor < len(stats)-1 {
			v.poolCursor++
		}
	case "k", "up":
		if v.poolCursor > 0 {
			v.poolCursor--
		}
	case "space":
		stats := v.statsFn()
		if v.poolCursor < len(stats) {
			k := stats[v.poolCursor].Key
			v.expanded[k] = !v.expanded[k]
		}
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
		if v.focused && i == v.poolCursor {
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

		// Expanded detail lines — multi-line, verbose, salience-driven
		if v.expanded[ps.Key] {
			details := v.poolDetail(ps)
			for _, d := range details {
				if rowCount >= poolLines {
					break
				}
				lines = append(lines, ansi.Truncate(mutedStyle.Render(d), v.width, ""))
				rowCount++
			}
		}
	}

	// Pad remaining pool lines
	for rowCount < poolLines {
		lines = append(lines, "")
		rowCount++
	}

	// -- Divider --
	events := v.eventsFn(100)
	divText := fmt.Sprintf("--- Activity (%d) ---", len(events))
	lines = append(lines, ansi.Truncate(mutedStyle.Render(divText), v.width, ""))

	// -- Activity feed (always global, reverse-chrono, wall-clock timestamps) --
	if feedHeight < 0 {
		feedHeight = 0
	}

	// Show newest first, up to feedHeight events
	feedEnd := len(events) - 1
	feedCount := 0
	for i := feedEnd; i >= 0 && feedCount < feedHeight; i-- {
		ev := events[i]
		ts := ev.Timestamp.Format("15:04:05")

		// Indicator + description
		var indicator, desc string
		switch ev.EventType {
		case data.FetchStart:
			indicator = primaryStyle.Render("~")
			desc = ev.PoolKey
		case data.FetchComplete:
			indicator = successStyle.Render("✓")
			desc = ev.PoolKey + " " + formatDuration(ev.Duration)
			if ev.DataSize > 0 {
				desc += " " + formatSize(ev.DataSize)
			}
		case data.FetchError:
			indicator = errorStyle.Render("✗")
			desc = ev.PoolKey
			if ev.Detail != "" {
				desc += " " + truncate(ev.Detail, 20)
			}
		case data.CacheHit:
			indicator = mutedStyle.Render("·")
			desc = ev.PoolKey + " hit"
		case data.CacheMiss:
			indicator = secondaryStyle.Render("·")
			desc = ev.PoolKey + " miss"
		case data.CacheSeeded:
			indicator = mutedStyle.Render("↑")
			desc = ev.PoolKey + " seeded"
		case data.PoolInvalidated:
			indicator = secondaryStyle.Render("↻")
			desc = ev.PoolKey + " stale"
		}

		line := mutedStyle.Render(ts) + " " + indicator + " " + desc
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

// poolDetail returns multi-line detail for an expanded pool, driven by salience.
func (v *PoolMonitor) poolDetail(ps data.PoolStatus) []string {
	var lines []string

	// Line 1: timing — poll interval, avg latency, data age
	var parts []string
	if ps.PollInterval > 0 {
		parts = append(parts, "poll "+formatDuration(ps.PollInterval))
	}
	if ps.AvgLatency > 0 {
		parts = append(parts, "avg "+formatDuration(ps.AvgLatency))
	}
	ageSrc := ps.FetchedAt
	if !ps.CachedFetchedAt.IsZero() {
		ageSrc = ps.CachedFetchedAt
	}
	if !ageSrc.IsZero() {
		parts = append(parts, "fetched "+ageSrc.Format("15:04:05"))
	}
	if len(parts) > 0 {
		lines = append(lines, "  "+strings.Join(parts, " · "))
	}

	// Line 2: reliability — fetches, errors, error rate
	parts = nil
	if ps.FetchCount > 0 {
		parts = append(parts, fmt.Sprintf("%d fetches", ps.FetchCount))
	}
	if ps.ErrorCount > 0 {
		rate := float64(ps.ErrorCount) / float64(ps.FetchCount+ps.ErrorCount) * 100
		parts = append(parts, fmt.Sprintf("%d errors (%.0f%%)", ps.ErrorCount, rate))
	}
	if len(parts) > 0 {
		lines = append(lines, "  "+strings.Join(parts, " · "))
	}

	// Line 3: freshness — hits, misses, hit ratio
	total := ps.HitCount + ps.MissCount
	if total > 0 {
		ratio := float64(ps.HitCount) / float64(total) * 100
		lines = append(lines, fmt.Sprintf("  %d hits · %d misses (%.0f%%)", ps.HitCount, ps.MissCount, ratio))
	}

	return lines
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// rjust right-justifies a (possibly ANSI-styled) string within the given width.
func rjust(s string, width int) string {
	pad := width - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

func formatSize(bytes int) string {
	if bytes <= 0 {
		return "-"
	}
	if bytes < 1000 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1000*1000 {
		return fmt.Sprintf("%dk", bytes/1000)
	}
	return fmt.Sprintf("%dM", bytes/1000/1000)
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours())/24)
}
