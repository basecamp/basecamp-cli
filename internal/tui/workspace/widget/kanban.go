package widget

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// KanbanCard represents a card within a column.
type KanbanCard struct {
	ID            string
	Title         string
	Assignees     string
	DueOn         string
	StepsProgress string // e.g. "3/5"
	CommentsCount int
	Completed     bool
}

// KanbanColumn represents a column in the kanban board.
type KanbanColumn struct {
	ID       string
	Title    string
	Color    string // Basecamp color name (red, blue, green, etc.)
	Deferred bool   // true for Done/NotNow columns (no cards fetched)
	Count    int    // total card count (including deferred)
	Items    []KanbanCard
}

// Kanban renders a horizontal multi-column kanban board.
type Kanban struct {
	styles  *tui.Styles
	columns []KanbanColumn
	width   int
	height  int

	// Focus
	colIdx  int // focused column index
	cardIdx int // focused card index within column
	focused bool

	// Per-column scroll offsets (first visible card index)
	scrolls []int
}

// NewKanban creates a new kanban board widget.
func NewKanban(styles *tui.Styles) *Kanban {
	return &Kanban{
		styles:  styles,
		focused: true,
	}
}

// SetColumns replaces all columns.
func (k *Kanban) SetColumns(cols []KanbanColumn) {
	k.columns = cols

	// Resize scroll offsets
	if len(k.scrolls) != len(cols) {
		k.scrolls = make([]int, len(cols))
	}

	// Clamp focus
	if k.colIdx >= len(cols) {
		k.colIdx = max(0, len(cols)-1)
	}
	if k.colIdx < len(cols) && k.cardIdx >= len(cols[k.colIdx].Items) {
		k.cardIdx = max(0, len(cols[k.colIdx].Items)-1)
	}
}

// SetSize updates dimensions.
func (k *Kanban) SetSize(w, h int) {
	k.width = w
	k.height = h
}

// SetFocused sets focus state.
func (k *Kanban) SetFocused(focused bool) {
	k.focused = focused
}

// FocusedColumn returns the index of the focused column.
func (k *Kanban) FocusedColumn() int {
	return k.colIdx
}

// FocusedCard returns the focused card, or nil.
func (k *Kanban) FocusedCard() *KanbanCard {
	if k.colIdx >= len(k.columns) {
		return nil
	}
	col := k.columns[k.colIdx]
	if k.cardIdx >= len(col.Items) {
		return nil
	}
	card := col.Items[k.cardIdx]
	return &card
}

// MoveLeft moves focus to the previous column.
func (k *Kanban) MoveLeft() {
	if k.colIdx > 0 {
		k.colIdx--
		k.clampCardIdx()
	}
}

// MoveRight moves focus to the next column.
func (k *Kanban) MoveRight() {
	if k.colIdx < len(k.columns)-1 {
		k.colIdx++
		k.clampCardIdx()
	}
}

// MoveUp moves focus to the previous card in the current column.
func (k *Kanban) MoveUp() {
	if k.cardIdx > 0 {
		k.cardIdx--
	}
}

// MoveDown moves focus to the next card in the current column.
func (k *Kanban) MoveDown() {
	if k.colIdx < len(k.columns) {
		col := k.columns[k.colIdx]
		if k.cardIdx < len(col.Items)-1 {
			k.cardIdx++
		}
	}
}

func (k *Kanban) clampCardIdx() {
	if k.colIdx >= len(k.columns) {
		return
	}
	col := k.columns[k.colIdx]
	if len(col.Items) == 0 {
		k.cardIdx = 0
	} else if k.cardIdx >= len(col.Items) {
		k.cardIdx = len(col.Items) - 1
	}
}

// minColWidth is the minimum usable width for a kanban column.
// Below this, column content becomes unreadable.
const minColWidth = 20

// View renders the kanban board. When all columns don't fit at minColWidth,
// shows a window of columns centered on the focused one.
func (k *Kanban) View() string {
	if k.width <= 0 || k.height <= 0 || len(k.columns) == 0 {
		return ""
	}

	theme := k.styles.Theme()
	numCols := len(k.columns)

	// How many columns fit at the minimum width?
	maxVisible := k.width / (minColWidth + 1) // +1 for divider
	if maxVisible < 1 {
		maxVisible = 1
	}
	if maxVisible > numCols {
		maxVisible = numCols
	}

	// Determine visible window centered on focused column
	startCol, endCol := k.visibleRange(numCols, maxVisible)
	visibleCols := endCol - startCol
	dividers := visibleCols - 1

	colWidth := (k.width - dividers) / visibleCols

	var rendered []string

	// Left overflow indicator
	if startCol > 0 {
		indicator := lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(1).
			Height(k.height).
			Render("◂")
		rendered = append(rendered, indicator)
		colWidth = (k.width - dividers - 2) / visibleCols // account for indicators
	}

	// Hard floor: avoid zero/negative widths that cause panics
	if colWidth < 6 {
		colWidth = 6
	}

	for i := startCol; i < endCol; i++ {
		col := k.columns[i]
		isFocusedCol := i == k.colIdx && k.focused
		rendered = append(rendered, k.renderColumn(col, i, colWidth, isFocusedCol, theme))
		if i < endCol-1 {
			divider := lipgloss.NewStyle().
				Foreground(theme.Border).
				Height(k.height).
				Render(strings.Repeat("│\n", k.height))
			rendered = append(rendered, divider)
		}
	}

	// Right overflow indicator
	if endCol < numCols {
		indicator := lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(1).
			Height(k.height).
			Render("▸")
		rendered = append(rendered, indicator)
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

// visibleRange returns the [start, end) range of columns to display,
// centered on the focused column.
func (k *Kanban) visibleRange(numCols, maxVisible int) (int, int) {
	if maxVisible >= numCols {
		return 0, numCols
	}

	// Center on focused column
	half := maxVisible / 2
	start := k.colIdx - half
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > numCols {
		end = numCols
		start = end - maxVisible
	}
	return start, end
}

func (k *Kanban) renderColumn(col KanbanColumn, colIndex, width int, isFocused bool, theme tui.Theme) string {
	var b strings.Builder

	// Header with column color
	headerFg := columnColor(col.Color, theme)
	if isFocused {
		headerFg = theme.Primary
	}
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Width(width).
		Foreground(headerFg)
	header := fmt.Sprintf("%s (%d)", col.Title, col.Count)
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	// Divider
	b.WriteString(lipgloss.NewStyle().
		Width(width).
		Foreground(theme.Border).
		Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	// Card area height: total height minus header (1) and divider (1)
	cardAreaHeight := k.height - 2
	if cardAreaHeight < 1 {
		cardAreaHeight = 1
	}

	if col.Deferred {
		// Deferred column: show count placeholder
		placeholder := fmt.Sprintf("  %d cards", col.Count)
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(width).
			Render(placeholder))
	} else if len(col.Items) == 0 {
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(width).
			Render("  (empty)"))
	} else {
		b.WriteString(k.renderCardArea(col, colIndex, width, cardAreaHeight, isFocused, theme))
	}

	return lipgloss.NewStyle().Width(width).Height(k.height).Render(b.String())
}

// renderCardArea renders the scrollable card list for a column.
func (k *Kanban) renderCardArea(col KanbanColumn, colIndex, width, areaHeight int, isFocused bool, theme tui.Theme) string {
	numCards := len(col.Items)

	// Ensure scroll offset array is large enough
	for len(k.scrolls) <= colIndex {
		k.scrolls = append(k.scrolls, 0)
	}

	// Calculate how many lines each card takes:
	// - unfocused: 1 line
	// - focused: 2 lines (title + detail)
	// We need to figure out which cards fit in the viewport.

	// First, adjust scroll to keep focused card visible
	if isFocused {
		k.adjustScroll(col, colIndex, areaHeight)
	}

	scrollOff := k.scrolls[colIndex]
	hasAbove := scrollOff > 0
	hasBelow := false

	var lines []string
	usedLines := 0
	maxLines := areaHeight
	if hasAbove {
		maxLines-- // reserve line for scroll-up indicator
	}

	for i := scrollOff; i < numCards; i++ {
		isCardFocused := isFocused && i == k.cardIdx
		cardLines := 1
		if isCardFocused {
			cardLines = focusedCardHeight(col.Items[i])
		}

		// Check if we need to reserve a line for scroll-down indicator
		remaining := numCards - i - 1
		needBelow := remaining > 0
		available := maxLines - usedLines
		if needBelow && cardLines >= available {
			hasBelow = true
			break
		}

		lines = append(lines, k.renderCompactCard(col.Items[i], width, isCardFocused, theme))
		usedLines += cardLines
		if usedLines >= maxLines {
			if i < numCards-1 {
				hasBelow = true
			}
			break
		}
	}

	var b strings.Builder
	if hasAbove {
		indicator := lipgloss.NewStyle().
			Width(width).
			Foreground(theme.Muted).
			Align(lipgloss.Center).
			Render("▲")
		b.WriteString(indicator)
		b.WriteString("\n")
	}
	b.WriteString(strings.Join(lines, "\n"))
	if hasBelow {
		b.WriteString("\n")
		indicator := lipgloss.NewStyle().
			Width(width).
			Foreground(theme.Muted).
			Align(lipgloss.Center).
			Render("▼")
		b.WriteString(indicator)
	}

	return b.String()
}

// adjustScroll ensures the focused card is visible in the viewport.
func (k *Kanban) adjustScroll(col KanbanColumn, colIndex, areaHeight int) {
	if k.cardIdx < 0 || k.cardIdx >= len(col.Items) {
		return
	}

	scroll := k.scrolls[colIndex]

	// If focused card is above viewport, scroll up
	if k.cardIdx < scroll {
		k.scrolls[colIndex] = k.cardIdx
		return
	}

	// Walk from scroll offset to see if focused card fits
	usedLines := 0
	for i := scroll; i < len(col.Items); i++ {
		cardLines := 1
		if i == k.cardIdx {
			cardLines = focusedCardHeight(col.Items[i])
		}

		// Reserve lines for scroll indicators
		available := areaHeight
		if scroll > 0 {
			available--
		}
		if i < len(col.Items)-1 {
			available-- // potential scroll-down indicator
		}

		usedLines += cardLines
		if i == k.cardIdx {
			if usedLines > available {
				// Focused card doesn't fit — scroll down
				k.scrolls[colIndex] = scroll + 1
				k.adjustScroll(col, colIndex, areaHeight) // recurse to converge
			}
			return
		}
	}
}

// renderCompactCard renders a card as 1 line (unfocused) or 1-2 lines (focused loupe).
// Returns 2 lines only when the focused card has detail metadata to show.
func (k *Kanban) renderCompactCard(card KanbanCard, width int, focused bool, theme tui.Theme) string {
	if focused {
		return k.renderFocusedCard(card, width, theme)
	}
	return k.renderUnfocusedCard(card, width, theme)
}

func (k *Kanban) renderFocusedCard(card KanbanCard, width int, theme tui.Theme) string {
	// Line 1: ▸ Full card title
	title := truncate(card.Title, width-4) // 2 for "▸ " + 2 padding
	titleStyle := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	line1 := titleStyle.Render("▸ " + title)
	result := lipgloss.NewStyle().Width(width).Render(line1)

	// Line 2: detail line (only if there's metadata)
	if detail := buildDetailLine(card); detail != "" {
		detailStyle := lipgloss.NewStyle().Foreground(theme.Muted)
		line2 := detailStyle.Render("  " + detail)
		result += "\n" + lipgloss.NewStyle().Width(width).Render(line2)
	}
	return result
}

func (k *Kanban) renderUnfocusedCard(card KanbanCard, width int, theme tui.Theme) string {
	title := truncate(card.Title, width-2) // 2 for "  " prefix
	style := lipgloss.NewStyle().Width(width)
	if card.Completed {
		style = style.Foreground(theme.Muted).Strikethrough(true)
	}
	return style.Render("  " + title)
}

// focusedCardHeight returns the number of lines a focused card occupies.
func focusedCardHeight(card KanbanCard) int {
	if buildDetailLine(card) != "" {
		return 2
	}
	return 1
}

// truncate shortens s to fit within maxLen characters, appending "..." if needed.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// buildDetailLine assembles the second line of a focused card loupe.
func buildDetailLine(card KanbanCard) string {
	var parts []string
	if card.Assignees != "" {
		parts = append(parts, card.Assignees)
	}
	if card.DueOn != "" {
		parts = append(parts, card.DueOn)
	}
	if card.StepsProgress != "" {
		parts = append(parts, card.StepsProgress)
	}
	if card.CommentsCount > 0 {
		parts = append(parts, fmt.Sprintf("%d comments", card.CommentsCount))
	}
	return strings.Join(parts, " \u00b7 ") // middle dot separator
}

// columnColor maps Basecamp color names to lipgloss colors.
func columnColor(name string, theme tui.Theme) lipgloss.AdaptiveColor {
	switch strings.ToLower(name) {
	case "red":
		return lipgloss.AdaptiveColor{Light: "#d93025", Dark: "#f28b82"}
	case "orange":
		return lipgloss.AdaptiveColor{Light: "#e8710a", Dark: "#fbbc04"}
	case "yellow":
		return lipgloss.AdaptiveColor{Light: "#f9ab00", Dark: "#fdd663"}
	case "green":
		return lipgloss.AdaptiveColor{Light: "#1e8e3e", Dark: "#81c995"}
	case "blue":
		return lipgloss.AdaptiveColor{Light: "#1a73e8", Dark: "#8ab4f8"}
	case "aqua":
		return lipgloss.AdaptiveColor{Light: "#12b5cb", Dark: "#78d9ec"}
	case "purple":
		return lipgloss.AdaptiveColor{Light: "#9334e6", Dark: "#c58af9"}
	case "pink":
		return lipgloss.AdaptiveColor{Light: "#e52592", Dark: "#ff8bcb"}
	case "brown":
		return lipgloss.AdaptiveColor{Light: "#795548", Dark: "#a1887f"}
	case "gray", "grey":
		return theme.Muted
	case "white":
		return theme.Foreground
	default:
		return theme.Foreground
	}
}
