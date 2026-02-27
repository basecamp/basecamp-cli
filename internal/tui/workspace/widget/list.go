// Package widget provides reusable composable sub-models for workspace views.
package widget

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
)

// ListItem represents a single item in an async list.
type ListItem struct {
	ID          string
	Title       string
	Description string
	Extra       string // right-aligned detail (count, date, etc.)
	Boosts      int    // number of boosts (will render as [♥ N])
	Marked      bool   // visual mark (star, check, etc.)
	Header      bool   // section header (non-selectable, rendered differently)
}

// FilterValue returns the string used for filtering.
func (i ListItem) FilterValue() string {
	return i.Title + " " + i.Description
}

// List is an async-capable list widget with filtering, scrolling, and selection.
type List struct {
	items    []ListItem
	filtered []ListItem
	cursor   int
	offset   int
	width    int
	height   int
	focused  bool
	loading  bool
	filter   string

	// Interactive filter mode
	filtering bool

	styles *tui.Styles
	keys   workspace.ListKeyMap

	// Callbacks
	emptyText string
	emptyMsg  *empty.Message
}

// NewList creates a new list widget.
func NewList(styles *tui.Styles) *List {
	return &List{
		styles:    styles,
		keys:      workspace.DefaultListKeyMap(),
		emptyText: "No items",
	}
}

// SetItems replaces the item list and resets the cursor.
func (l *List) SetItems(items []ListItem) {
	l.items = items
	l.applyFilter()
	l.loading = false

	// Clamp cursor and skip headers
	if l.cursor >= len(l.filtered) {
		l.cursor = max(0, len(l.filtered)-1)
	}
	l.skipHeaders(1)
}

// SetLoading puts the list in loading state.
func (l *List) SetLoading(loading bool) {
	l.loading = loading
}

// SetEmptyText sets the message shown when no items exist.
func (l *List) SetEmptyText(text string) {
	l.emptyText = text
}

// SetEmptyMessage sets a structured empty state with title, body, and hints.
func (l *List) SetEmptyMessage(msg empty.Message) {
	l.emptyMsg = &msg
}

// SetSize updates dimensions.
func (l *List) SetSize(w, h int) {
	l.width = w
	l.height = h
}

// SetFocused sets focus state.
func (l *List) SetFocused(focused bool) {
	l.focused = focused
}

// Selected returns the currently highlighted item, or nil.
func (l *List) Selected() *ListItem {
	if l.cursor < 0 || l.cursor >= len(l.filtered) {
		return nil
	}
	item := l.filtered[l.cursor]
	return &item
}

// SelectedIndex returns the cursor position.
func (l *List) SelectedIndex() int {
	return l.cursor
}

// Items returns the current (possibly filtered) items.
func (l *List) Items() []ListItem {
	return l.filtered
}

// Len returns the number of visible items.
func (l *List) Len() int {
	return len(l.filtered)
}

// SelectByID scans filtered items for a matching ID, sets cursor + adjusts offset.
// Returns true if found, false if not.
func (l *List) SelectByID(id string) bool {
	for i, item := range l.filtered {
		if item.ID == id {
			l.cursor = i
			l.skipHeaders(1)
			l.clampOffset()
			return true
		}
	}
	return false
}

// SelectIndex sets the cursor to the given index (clamped to bounds).
func (l *List) SelectIndex(idx int) {
	if len(l.filtered) == 0 {
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(l.filtered) {
		idx = len(l.filtered) - 1
	}
	l.cursor = idx
	l.skipHeaders(1)
	l.clampOffset()
}

// clampOffset ensures the cursor is visible within the viewport.
func (l *List) clampOffset() {
	visibleHeight := l.visibleHeight()
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+visibleHeight {
		l.offset = l.cursor - visibleHeight + 1
	}
}

// StartFilter enters interactive filter mode.
func (l *List) StartFilter() {
	l.filtering = true
	l.filter = ""
	l.applyFilter()
	l.cursor = 0
	l.offset = 0
	l.skipHeaders(1)
}

// StopFilter exits interactive filter mode and restores all items.
func (l *List) StopFilter() {
	l.filtering = false
	l.filter = ""
	l.applyFilter()
	l.cursor = 0
	l.offset = 0
	l.skipHeaders(1)
}

// Filtering returns whether interactive filter mode is active.
func (l *List) Filtering() bool {
	return l.filtering
}

// Update handles key events for list navigation.
func (l *List) Update(msg tea.Msg) tea.Cmd {
	if !l.focused {
		return nil
	}

	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}

	if l.filtering {
		return l.handleFilterKey(km)
	}

	visibleHeight := l.visibleHeight()

	switch {
	case key.Matches(km, l.keys.Up):
		l.moveCursor(-1)
	case key.Matches(km, l.keys.Down):
		l.moveCursor(1)
	case key.Matches(km, l.keys.Top):
		l.cursor = 0
		l.skipHeaders(1)
		l.offset = 0
	case key.Matches(km, l.keys.Bottom):
		l.cursor = max(0, len(l.filtered)-1)
		l.skipHeaders(-1)
		if l.cursor >= visibleHeight {
			l.offset = l.cursor - visibleHeight + 1
		}
	case key.Matches(km, l.keys.PageDown):
		l.cursor += visibleHeight / 2
		if l.cursor >= len(l.filtered) {
			l.cursor = max(0, len(l.filtered)-1)
		}
		l.skipHeaders(1)
		if l.cursor >= l.offset+visibleHeight {
			l.offset = l.cursor - visibleHeight + 1
		}
	case key.Matches(km, l.keys.PageUp):
		l.cursor -= visibleHeight / 2
		if l.cursor < 0 {
			l.cursor = 0
		}
		l.skipHeaders(-1)
		if l.cursor < l.offset {
			l.offset = l.cursor
		}
	default:
		return nil
	}
	return nil
}

// moveCursor moves by delta (+1 or -1), skipping header items.
func (l *List) moveCursor(delta int) {
	n := len(l.filtered)
	if n == 0 {
		return
	}
	next := l.cursor + delta
	for next >= 0 && next < n && l.filtered[next].Header {
		next += delta
	}
	if next < 0 || next >= n {
		return
	}
	l.cursor = next
	visibleHeight := l.visibleHeight()
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+visibleHeight {
		l.offset = l.cursor - visibleHeight + 1
	}
}

// skipHeaders nudges cursor in direction until it lands on a non-header item.
func (l *List) skipHeaders(direction int) {
	n := len(l.filtered)
	for l.cursor >= 0 && l.cursor < n && l.filtered[l.cursor].Header {
		l.cursor += direction
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= n {
		l.cursor = max(0, n-1)
	}
}

// handleFilterKey processes keys during interactive filter mode.
func (l *List) handleFilterKey(km tea.KeyMsg) tea.Cmd {
	switch km.String() {
	case "esc":
		l.StopFilter()
	case "backspace":
		if l.filter == "" {
			l.StopFilter()
		} else {
			runes := []rune(l.filter)
			l.filter = string(runes[:len(runes)-1])
			l.applyFilter()
			l.cursor = 0
			l.offset = 0
			l.skipHeaders(1)
		}
	case "up", "k":
		l.moveCursor(-1)
	case "down", "j":
		l.moveCursor(1)
	case "enter":
		l.filtering = false
		// Keep filter applied
	default:
		if km.Type == tea.KeyRunes {
			l.filter += string(km.Runes)
			l.applyFilter()
			l.cursor = 0
			l.offset = 0
			l.skipHeaders(1)
		}
	}
	return nil
}

func (l *List) visibleHeight() int {
	h := l.height
	if h <= 0 {
		h = 10
	}
	if l.filtering || l.filter != "" {
		h--
	}
	// Reserve 1 line for scroll indicator when items exceed viewport
	if len(l.filtered) > h {
		h--
	}
	if h < 1 {
		h = 1
	}
	return h
}

func (l *List) applyFilter() {
	if l.filter == "" {
		l.filtered = l.items
		return
	}
	l.filtered = nil
	for _, item := range l.items {
		if item.Header {
			continue // skip headers when filtering
		}
		if fuzzyMatch(item.FilterValue(), l.filter) {
			l.filtered = append(l.filtered, item)
		}
	}
}

// fuzzyMatch returns true if query is a subsequence of s (case-insensitive).
func fuzzyMatch(s, query string) bool {
	s = strings.ToLower(s)
	queryRunes := []rune(strings.ToLower(query))
	qi := 0
	for _, r := range s {
		if qi < len(queryRunes) && r == queryRunes[qi] {
			qi++
		}
	}
	return qi == len(queryRunes)
}

// View renders the list.
func (l *List) View() string {
	if l.width <= 0 || l.height <= 0 {
		return ""
	}

	theme := l.styles.Theme()

	if l.loading {
		return lipgloss.NewStyle().
			Width(l.width).
			Foreground(theme.Muted).
			Render("Loading…")
	}

	var b strings.Builder

	// Filter bar
	if l.filtering || l.filter != "" {
		prefix := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("/")
		filterText := l.filter
		cursor := ""
		if l.filtering {
			cursor = lipgloss.NewStyle().Foreground(theme.Primary).Render("\u2588")
		}
		counts := lipgloss.NewStyle().Foreground(theme.Muted).
			Render(fmt.Sprintf("%d/%d", len(l.filtered), len(l.items)))

		countsWidth := lipgloss.Width(counts)
		prefixWidth := lipgloss.Width(prefix)
		cursorWidth := lipgloss.Width(cursor)
		maxFilterWidth := l.width - countsWidth - prefixWidth - cursorWidth - 2
		if maxFilterWidth > 0 && lipgloss.Width(filterText) > maxFilterWidth {
			filterText = Truncate(filterText, maxFilterWidth)
		}

		left := prefix + filterText + cursor
		leftWidth := lipgloss.Width(left)
		gap := l.width - leftWidth - countsWidth
		if gap < 1 {
			gap = 1
		}
		b.WriteString(left + strings.Repeat(" ", gap) + counts)
		b.WriteString("\n")
	}

	if len(l.filtered) == 0 {
		if l.filter != "" {
			b.WriteString(lipgloss.NewStyle().
				Width(l.width).
				Foreground(theme.Muted).
				Render("No matches"))
			return b.String()
		}
		if l.emptyMsg != nil {
			b.WriteString(l.renderEmptyMessage(theme))
			return b.String()
		}
		b.WriteString(lipgloss.NewStyle().
			Width(l.width).
			Foreground(theme.Muted).
			Render(l.emptyText))
		return b.String()
	}

	visibleHeight := l.visibleHeight()
	end := l.offset + visibleHeight
	if end > len(l.filtered) {
		end = len(l.filtered)
	}

	for i := l.offset; i < end; i++ {
		item := l.filtered[i]
		isSelected := i == l.cursor && l.focused

		line := l.renderItem(item, isSelected, theme)
		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}

	// Scroll indicator
	if len(l.filtered) > visibleHeight {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render(fmt.Sprintf(" %d/%d", l.cursor+1, len(l.filtered))))
	}

	return b.String()
}

func (l *List) renderEmptyMessage(theme tui.Theme) string {
	var lines []string
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Foreground)
	bodyStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	hintStyle := lipgloss.NewStyle().Foreground(theme.Secondary)

	lines = append(lines, titleStyle.Render(l.emptyMsg.Title))
	if l.emptyMsg.Body != "" {
		lines = append(lines, bodyStyle.Render(l.emptyMsg.Body))
	}
	if len(l.emptyMsg.Hints) > 0 {
		lines = append(lines, "")
		for _, hint := range l.emptyMsg.Hints {
			lines = append(lines, hintStyle.Render("  "+hint))
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (l *List) renderItem(item ListItem, selected bool, theme tui.Theme) string {
	// Section headers render as non-selectable dividers
	if item.Header {
		headerStyle := lipgloss.NewStyle().Foreground(theme.Muted).Bold(true).MaxWidth(l.width)
		line := "── " + item.Title + " ──"
		return headerStyle.Render(line)
	}

	cursor := "  "
	titleStyle := lipgloss.NewStyle().Foreground(theme.Foreground)
	descStyle := lipgloss.NewStyle().Foreground(theme.Muted)

	if selected {
		cursor = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("> ")
		titleStyle = titleStyle.Bold(true).Foreground(theme.Primary)
	}

	title := item.Title

	// Truncate title if it would overflow available width
	cursorWidth := lipgloss.Width(cursor)
	maxTitleWidth := l.width - cursorWidth
	if item.Marked {
		maxTitleWidth -= 2 // "* " prefix
	}
	if item.Boosts > 0 {
		maxTitleWidth -= lipgloss.Width(fmt.Sprintf(" [♥ %d]", item.Boosts))
	}
	if item.Extra != "" {
		maxTitleWidth -= lipgloss.Width(item.Extra) + 2 // extra + gap
	}
	if maxTitleWidth > 0 {
		title = Truncate(title, maxTitleWidth)
	}

	if item.Marked {
		title = lipgloss.NewStyle().Foreground(theme.Warning).Render("* ") + title
	}

	line := cursor + titleStyle.Render(title)
	if item.Boosts > 0 {
		boostStr := fmt.Sprintf(" [♥ %d]", item.Boosts)
		line += lipgloss.NewStyle().Foreground(theme.Success).Render(boostStr)
	}

	// Add extra (right-aligned) if space permits
	if item.Extra != "" {
		extra := descStyle.Render(item.Extra)
		titleWidth := lipgloss.Width(line)
		extraWidth := lipgloss.Width(extra)
		gap := l.width - titleWidth - extraWidth
		if gap > 1 {
			// Fit description between title and extra when both are present.
			// Reserve at least 2 chars of gap so extra always renders.
			if item.Description != "" && gap > 4 {
				maxDesc := gap - 3 // 1 leading space + 2 minimum gap
				desc := " " + Truncate(item.Description, maxDesc)
				line += descStyle.Render(desc)
			}
			gap = l.width - lipgloss.Width(line) - extraWidth
			if gap > 0 {
				line += strings.Repeat(" ", gap) + extra
			}
		}
	}

	// Add description inline (after title) when no extra badge
	if item.Description != "" && item.Extra == "" {
		avail := l.width - lipgloss.Width(line)
		if avail > 3 {
			desc := item.Description
			rendered := descStyle.Render(" " + desc)
			if lipgloss.Width(rendered) > avail {
				// Truncate to fit with ellipsis
				for len(desc) > 0 && lipgloss.Width(descStyle.Render(" "+desc+"…")) > avail {
					desc = desc[:len(desc)-1]
				}
				if len(desc) > 0 {
					line += descStyle.Render(" " + desc + "…")
				}
			} else {
				line += rendered
			}
		}
	}

	return line
}
