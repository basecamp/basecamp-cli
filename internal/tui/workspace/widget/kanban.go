package widget

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// KanbanColumn represents a column in the kanban board.
type KanbanColumn struct {
	ID    string
	Title string
	Items []KanbanCard
}

// KanbanCard represents a card within a column.
type KanbanCard struct {
	ID       string
	Title    string
	Subtitle string // assignee, due date, etc.
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
	if k.cardIdx >= len(col.Items) {
		k.cardIdx = max(0, len(col.Items)-1)
	}
}

// View renders the kanban board.
func (k *Kanban) View() string {
	if k.width <= 0 || k.height <= 0 || len(k.columns) == 0 {
		return ""
	}

	theme := k.styles.Theme()
	numCols := len(k.columns)

	// Responsive: if too narrow, show one column at a time
	if k.width < 40*numCols {
		return k.renderSingleColumn(theme)
	}

	colWidth := (k.width - numCols + 1) / numCols // account for dividers
	if colWidth < 20 {
		colWidth = 20
	}

	var rendered []string
	for i, col := range k.columns {
		isFocusedCol := i == k.colIdx && k.focused
		rendered = append(rendered, k.renderColumn(col, i, colWidth, isFocusedCol, theme))
		if i < numCols-1 {
			divider := lipgloss.NewStyle().
				Foreground(theme.Border).
				Height(k.height).
				Render(strings.Repeat("│\n", k.height))
			rendered = append(rendered, divider)
		}
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, rendered...)
}

func (k *Kanban) renderColumn(col KanbanColumn, _, width int, isFocused bool, theme tui.Theme) string {
	var b strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Width(width).
		Foreground(theme.Foreground)
	if isFocused {
		headerStyle = headerStyle.Foreground(theme.Primary)
	}
	header := fmt.Sprintf("%s (%d)", col.Title, len(col.Items))
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().
		Width(width).
		Foreground(theme.Border).
		Render(strings.Repeat("─", width)))
	b.WriteString("\n")

	// Cards
	if len(col.Items) == 0 {
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(width).
			Render("  (empty)"))
	} else {
		for j, card := range col.Items {
			isCardFocused := isFocused && j == k.cardIdx
			b.WriteString(k.renderCard(card, width, isCardFocused, theme))
			if j < len(col.Items)-1 {
				b.WriteString("\n")
			}
		}
	}

	return lipgloss.NewStyle().Width(width).Height(k.height).Render(b.String())
}

func (k *Kanban) renderCard(card KanbanCard, width int, focused bool, theme tui.Theme) string {
	borderStyle := lipgloss.RoundedBorder()
	borderColor := theme.Border
	if focused {
		borderColor = theme.Primary
	}

	cardStyle := lipgloss.NewStyle().
		Border(borderStyle).
		BorderForeground(borderColor).
		Width(width-4). // account for border + padding
		Padding(0, 1)

	title := card.Title
	if len(title) > width-6 {
		title = title[:width-9] + "..."
	}

	content := title
	if card.Subtitle != "" {
		content += "\n" + lipgloss.NewStyle().Foreground(theme.Muted).Render(card.Subtitle)
	}

	return cardStyle.Render(content)
}

func (k *Kanban) renderSingleColumn(theme tui.Theme) string {
	if k.colIdx >= len(k.columns) {
		return ""
	}
	col := k.columns[k.colIdx]

	var b strings.Builder

	// Navigation indicator
	nav := lipgloss.NewStyle().Foreground(theme.Muted).Render(
		fmt.Sprintf("< %d/%d >", k.colIdx+1, len(k.columns)))
	b.WriteString(nav)
	b.WriteString("\n")

	return b.String() + k.renderColumn(col, k.colIdx, k.width, true, theme)
}
