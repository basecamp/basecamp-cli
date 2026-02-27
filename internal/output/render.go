package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Renderer handles styled terminal output.
type Renderer struct {
	width  int
	styled bool // whether to emit ANSI styling

	// Text styles
	Summary lipgloss.Style
	Muted   lipgloss.Style
	Data    lipgloss.Style
	Error   lipgloss.Style
	Hint    lipgloss.Style
	Warning lipgloss.Style
	Success lipgloss.Style
	Subtle  lipgloss.Style // for footer elements (most understated)

	// Table styles
	Header    lipgloss.Style
	Cell      lipgloss.Style
	CellMuted lipgloss.Style
}

// NewRenderer creates a renderer with styles from the resolved theme.
// Styling is enabled when writing to a TTY, or when forceStyled is true.
// Theme resolution follows: NO_COLOR env → BASECAMP_THEME env → user theme
// (~/.config/basecamp/theme/colors.toml, which may be symlinked to system themes) → default.
func NewRenderer(w io.Writer, forceStyled bool) *Renderer {
	return NewRendererWithTheme(w, forceStyled, tui.ResolveTheme())
}

// NewRendererWithTheme creates a renderer with a specific theme (for testing).
func NewRendererWithTheme(w io.Writer, forceStyled bool, theme tui.Theme) *Renderer {
	width, isTTY := terminalInfo(w)
	styled := isTTY || forceStyled

	// Set global color profile based on styled flag
	// Note: This is a workaround because lipgloss.NewRenderer doesn't properly
	// pass through the color profile in this version
	if styled {
		lipgloss.SetColorProfile(2) // TrueColor
	} else {
		lipgloss.SetColorProfile(0) // Ascii (no colors)
	}

	r := &Renderer{
		width:  width,
		styled: styled,
	}

	if styled {
		// Use AdaptiveColor directly — lipgloss resolves Light vs Dark
		// based on terminal background detection.
		r.Summary = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
		r.Muted = lipgloss.NewStyle().Foreground(theme.Muted)
		r.Data = lipgloss.NewStyle().Foreground(theme.Foreground)
		r.Error = lipgloss.NewStyle().Foreground(theme.Error).Bold(true)
		r.Hint = lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
		r.Warning = lipgloss.NewStyle().Foreground(theme.Warning)
		r.Success = lipgloss.NewStyle().Foreground(theme.Success)
		r.Subtle = lipgloss.NewStyle().Foreground(theme.Border)
		r.Header = lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)
		r.Cell = lipgloss.NewStyle().Foreground(theme.Foreground)
		r.CellMuted = lipgloss.NewStyle().Foreground(theme.Muted)
	} else {
		// Plain text - no styling
		r.Summary = lipgloss.NewStyle()
		r.Muted = lipgloss.NewStyle()
		r.Data = lipgloss.NewStyle()
		r.Error = lipgloss.NewStyle()
		r.Hint = lipgloss.NewStyle()
		r.Warning = lipgloss.NewStyle()
		r.Success = lipgloss.NewStyle()
		r.Subtle = lipgloss.NewStyle()
		r.Header = lipgloss.NewStyle()
		r.Cell = lipgloss.NewStyle()
		r.CellMuted = lipgloss.NewStyle()
	}

	return r
}

// terminalInfo returns the terminal width and whether the writer is a TTY.
func terminalInfo(w io.Writer) (width int, isTTY bool) {
	width = 80 // default

	if f, ok := w.(*os.File); ok {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w >= 40 {
			width = w
		}
		// Check if it's a TTY
		fi, err := f.Stat()
		if err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
			isTTY = true
		}
	}

	return width, isTTY
}

// RenderResponse renders a success response to the writer.
func (r *Renderer) RenderResponse(w io.Writer, resp *Response) error {
	var b strings.Builder

	// Summary line
	if resp.Summary != "" {
		b.WriteString(r.Summary.Render(resp.Summary))
		b.WriteString("\n")
	}

	// Notice (e.g., truncation warning)
	if resp.Notice != "" {
		b.WriteString(r.Hint.Render(resp.Notice))
		b.WriteString("\n")
	}

	if resp.Summary != "" || resp.Notice != "" {
		b.WriteString("\n")
	}

	// Main data
	data := NormalizeData(resp.Data)
	r.renderData(&b, data)

	// Footer separator (divider before breadcrumbs/stats)
	hasFooter := len(resp.Breadcrumbs) > 0 || extractStats(resp.Meta) != nil
	if hasFooter {
		b.WriteString("\n")
		b.WriteString(r.Muted.Render("─────"))
		b.WriteString("\n")
	}

	// Breadcrumbs
	if len(resp.Breadcrumbs) > 0 {
		r.renderBreadcrumbs(&b, resp.Breadcrumbs)
	}

	// Stats (from --stats flag)
	if stats := extractStats(resp.Meta); stats != nil {
		r.renderStats(&b, stats)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// RenderError renders an error response to the writer with a styled error box.
func (r *Renderer) RenderError(w io.Writer, resp *ErrorResponse) error {
	var b strings.Builder

	if r.styled {
		// Create a styled error box with border
		errorIcon := "✗"
		errorTitle := errorIcon + " Error"

		// Wrap error message to fit in box (accounting for border and padding)
		maxWidth := max(
			// border (2) + padding (2)
			r.width-4, 40)

		errorMsg := wrapText(resp.Error, maxWidth)

		// Build content lines
		var contentLines []string
		contentLines = append(contentLines, r.Error.Bold(true).Render(errorTitle))
		contentLines = append(contentLines, "")
		for line := range strings.SplitSeq(errorMsg, "\n") {
			contentLines = append(contentLines, r.Data.Render(line))
		}

		if resp.Hint != "" {
			contentLines = append(contentLines, "")
			hintMsg := wrapText(resp.Hint, maxWidth)
			for i, line := range strings.Split(hintMsg, "\n") {
				if i == 0 {
					contentLines = append(contentLines, r.Hint.Render("→ "+line))
				} else {
					contentLines = append(contentLines, r.Hint.Render("  "+line))
				}
			}
		}

		// Create bordered box with error color border
		boxStyle := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(r.Error.GetForeground()).
			Padding(0, 1)

		content := strings.Join(contentLines, "\n")
		b.WriteString(boxStyle.Render(content))
		b.WriteString("\n")
	} else {
		// Plain text output (no styling)
		b.WriteString("Error: " + resp.Error)
		b.WriteString("\n")

		if resp.Hint != "" {
			b.WriteString("Hint: " + resp.Hint)
			b.WriteString("\n")
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// wrapText wraps text to fit within maxWidth, preserving words and newlines.
// Uses rune counting for proper Unicode support.
func wrapText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		maxWidth = 80
	}

	// Split on existing newlines first to preserve structure
	paragraphs := strings.Split(text, "\n")
	var result []string

	for _, para := range paragraphs {
		if para == "" {
			result = append(result, "")
			continue
		}

		words := strings.Fields(para)
		if len(words) == 0 {
			result = append(result, "")
			continue
		}

		var currentLine strings.Builder
		currentWidth := 0

		for _, word := range words {
			wordWidth := runeWidth(word)

			// Handle words longer than maxWidth by adding them on their own line
			if wordWidth > maxWidth {
				if currentLine.Len() > 0 {
					result = append(result, currentLine.String())
					currentLine.Reset()
					currentWidth = 0
				}
				result = append(result, word)
				continue
			}

			// If adding this word would exceed width, start new line
			if currentWidth+1+wordWidth > maxWidth && currentLine.Len() > 0 {
				result = append(result, currentLine.String())
				currentLine.Reset()
				currentWidth = 0
			}

			// Add word to current line
			if currentLine.Len() > 0 {
				currentLine.WriteString(" ")
				currentWidth++
			}
			currentLine.WriteString(word)
			currentWidth += wordWidth
		}

		// Don't forget the last line
		if currentLine.Len() > 0 {
			result = append(result, currentLine.String())
		}
	}

	return strings.Join(result, "\n")
}

// runeWidth returns the display width of a string, counting runes.
func runeWidth(s string) int {
	return utf8.RuneCountInString(s)
}

func (r *Renderer) renderData(b *strings.Builder, data any) {
	switch d := data.(type) {
	case []map[string]any:
		if len(d) == 0 {
			b.WriteString(r.Muted.Render("(no results)"))
			b.WriteString("\n")
			return
		}
		r.renderTable(b, d)

	case map[string]any:
		r.renderObject(b, d)

	case []any:
		if len(d) == 0 {
			b.WriteString(r.Muted.Render("(no results)"))
			b.WriteString("\n")
			return
		}
		// Try to convert to []map[string]any
		if maps := toMapSlice(d); maps != nil {
			r.renderTable(b, maps)
		} else {
			r.renderList(b, d)
		}

	case string:
		b.WriteString(r.Data.Render(ansi.Strip(d)))
		b.WriteString("\n")

	case nil:
		b.WriteString(r.Muted.Render("(no data)"))
		b.WriteString("\n")

	default:
		// Fallback: format as string
		b.WriteString(r.Data.Render(ansi.Strip(fmt.Sprintf("%v", data))))
		b.WriteString("\n")
	}
}

func toMapSlice(slice []any) []map[string]any {
	if len(slice) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(slice))
	for _, item := range slice {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		} else {
			return nil
		}
	}
	return result
}

// Column priority for table rendering (lower = higher priority)
var columnPriority = map[string]int{
	"id":          1,
	"name":        2,
	"title":       2,
	"content":     3,
	"status":      4,
	"completed":   4,
	"due_on":      5,
	"due_date":    5,
	"assignees":   6,
	"description": 7,
	"created_at":  8,
	"updated_at":  9,
}

// Columns to render in muted style
var mutedColumns = map[string]bool{
	"id":         true,
	"created_at": true,
	"updated_at": true,
}

// Columns to skip (nested objects, internal fields)
var skipColumns = map[string]bool{
	"bucket":          true,
	"creator":         true,
	"parent":          true,
	"dock":            true,
	"inherits_status": true,
	"url":             true,
	"app_url":         true,
}

type column struct {
	key      string
	header   string
	priority int
	muted    bool
	width    int
}

func (r *Renderer) renderTable(b *strings.Builder, data []map[string]any) {
	if len(data) == 0 {
		return
	}

	// Detect columns from first row
	columns := r.detectColumns(data)
	if len(columns) == 0 {
		return
	}

	// Select columns that fit terminal width
	columns = r.selectColumns(columns, data)

	// Build table
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return r.Header
			}
			if col < len(columns) && columns[col].muted {
				return r.CellMuted
			}
			return r.Cell
		})

	// Headers
	headers := make([]string, len(columns))
	for i, col := range columns {
		headers[i] = col.header
	}
	t.Headers(headers...)

	// Rows
	for _, item := range data {
		row := make([]string, len(columns))
		for i, col := range columns {
			row[i] = formatCell(item[col.key])
		}
		t.Row(row...)
	}

	b.WriteString(t.String())
	b.WriteString("\n")
}

func (r *Renderer) detectColumns(data []map[string]any) []column {
	if len(data) == 0 {
		return nil
	}

	first := data[0]
	var cols []column

	for key, val := range first {
		if skipColumns[key] {
			continue
		}

		// Skip nested objects
		switch val.(type) {
		case map[string]any:
			continue
		case []map[string]any:
			continue
		case []any:
			// Allow assignees, skip other arrays
			if key != "assignees" {
				continue
			}
		}

		priority := columnPriority[key]
		if priority == 0 {
			priority = 50
		}

		cols = append(cols, column{
			key:      key,
			header:   formatHeader(key),
			priority: priority,
			muted:    mutedColumns[key],
		})
	}

	// Sort by priority
	sort.Slice(cols, func(i, j int) bool {
		return cols[i].priority < cols[j].priority
	})

	return cols
}

func (r *Renderer) selectColumns(cols []column, data []map[string]any) []column {
	if len(cols) == 0 {
		return cols
	}

	// Calculate widths
	for i := range cols {
		cols[i].width = lipgloss.Width(cols[i].header)
		for _, row := range data {
			cellWidth := lipgloss.Width(formatCell(row[cols[i].key]))
			if cellWidth > cols[i].width {
				cols[i].width = cellWidth
			}
		}
		// Cap width at 40 for long content
		if cols[i].width > 40 {
			cols[i].width = 40
		}
	}

	// Remove columns until we fit
	padding := 2
	selected := make([]column, len(cols))
	copy(selected, cols)

	for len(selected) > 1 {
		total := 0
		for _, col := range selected {
			total += col.width + padding
		}
		if total <= r.width {
			break
		}
		selected = selected[:len(selected)-1]
	}

	return selected
}

// renderField represents a field to render with its priority for ordering.
type renderField struct {
	key      string
	priority int
}

// Columns to skip in object rendering (internal fields, nested objects)
var skipObjectColumns = map[string]bool{
	"bucket":           true,
	"creator":          true,
	"parent":           true,
	"dock":             true,
	"inherits_status":  true,
	"url":              true,
	"app_url":          true,
	"bookmark_url":     true,
	"subscription_url": true,
	"comments_count":   true,
	"comments_url":     true,
	"position":         true,
	"attachable_sgid":  true,
	"personable_type":  true,
	"recording_type":   true,
}

func (r *Renderer) renderObject(b *strings.Builder, data map[string]any) {
	// Collect fields with priority ordering
	var fields []renderField

	for k := range data {
		if skipObjectColumns[k] {
			continue
		}
		// Skip nested objects
		switch data[k].(type) {
		case map[string]any, []map[string]any:
			continue
		}
		priority := columnPriority[k]
		if priority == 0 {
			priority = 50
		}
		fields = append(fields, renderField{key: k, priority: priority})
	}

	// Sort by priority (lower = higher priority)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].priority != fields[j].priority {
			return fields[i].priority < fields[j].priority
		}
		return fields[i].key < fields[j].key
	})

	if len(fields) == 0 {
		b.WriteString(r.Muted.Render("(no data)"))
		b.WriteString("\n")
		return
	}

	// Find max label length for alignment (using formatted headers)
	maxLen := 0
	for _, f := range fields {
		label := formatHeader(f.key)
		if len(label) > maxLen {
			maxLen = len(label)
		}
	}

	for _, f := range fields {
		label := formatHeader(f.key)
		labelStyled := r.Muted.Render(fmt.Sprintf("%-*s: ", maxLen, label))

		// Apply muted styling to metadata fields
		var valueStyled string
		if mutedColumns[f.key] {
			valueStyled = r.CellMuted.Render(formatDateValue(f.key, data[f.key]))
		} else {
			valueStyled = r.Data.Render(formatDateValue(f.key, data[f.key]))
		}
		b.WriteString(labelStyled + valueStyled + "\n")
	}
}

func (r *Renderer) renderList(b *strings.Builder, data []any) {
	for _, item := range data {
		b.WriteString(r.Data.Render("• " + formatCell(item)))
		b.WriteString("\n")
	}
}

func (r *Renderer) renderBreadcrumbs(b *strings.Builder, crumbs []Breadcrumb) {
	b.WriteString(r.Muted.Render("Hints:"))
	b.WriteString("\n")
	for _, bc := range crumbs {
		line := r.Data.Render("  " + bc.Cmd)
		if bc.Description != "" {
			line += r.Subtle.Render("  # " + bc.Description)
		}
		b.WriteString(line + "\n")
	}
}

// renderStats renders session statistics in a compact one-liner.
func (r *Renderer) renderStats(b *strings.Builder, stats map[string]any) {
	metrics := observability.SessionMetricsFromMap(stats)
	parts := metrics.FormatParts()
	if len(parts) > 0 {
		line := r.Subtle.Render(strings.Join(parts, " · "))
		b.WriteString(line + "\n")
	}
}

func formatHeader(key string) string {
	key = strings.ReplaceAll(key, "_", " ")
	key = strings.TrimSuffix(key, " on")
	key = strings.TrimSuffix(key, " at")
	// Simple title case
	words := strings.Fields(key)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func formatCell(val any) string {
	switch v := val.(type) {
	case nil:
		return ""
	case string:
		v = ansi.Strip(v)
		// Truncate long strings (rune-safe for multi-byte UTF-8)
		if utf8.RuneCountInString(v) > 40 {
			runes := []rune(v)
			return string(runes[:37]) + "..."
		}
		return v
	case bool:
		if v {
			return "yes"
		}
		return "no"
	case json.Number:
		return v.String()
	case float64:
		if v == float64(int(v)) {
			return fmt.Sprintf("%d", int(v))
		}
		return fmt.Sprintf("%.2f", v)
	case int, int64:
		return fmt.Sprintf("%d", v)
	case []any:
		// Handle arrays (assignees, tags, etc.)
		if len(v) == 0 {
			return ""
		}
		var items []string
		for _, item := range v {
			switch elem := item.(type) {
			case string:
				items = append(items, ansi.Strip(elem))
			case json.Number:
				items = append(items, elem.String())
			case float64:
				if elem == float64(int(elem)) {
					items = append(items, fmt.Sprintf("%d", int(elem)))
				} else {
					items = append(items, fmt.Sprintf("%.2f", elem))
				}
			case int, int64:
				items = append(items, fmt.Sprintf("%d", elem))
			case map[string]any:
				// Try name, then title, then id, then fallback
				if name, ok := elem["name"].(string); ok {
					items = append(items, ansi.Strip(name))
				} else if title, ok := elem["title"].(string); ok {
					items = append(items, ansi.Strip(title))
				} else if id, ok := elem["id"]; ok {
					items = append(items, fmt.Sprintf("%v", id))
				}
			default:
				items = append(items, ansi.Strip(fmt.Sprintf("%v", item)))
			}
		}
		return strings.Join(items, ", ")
	default:
		return ansi.Strip(fmt.Sprintf("%v", v))
	}
}

// formatDateValue formats date fields in a human-readable way.
// For date columns (created_at, updated_at, due_on, due_date), it converts
// ISO8601 timestamps to a more readable format.
func formatDateValue(key string, val any) string {
	// Check if this is a date column
	isDateColumn := strings.HasSuffix(key, "_at") || strings.HasSuffix(key, "_on") || strings.HasSuffix(key, "_date")

	if !isDateColumn {
		return formatCell(val)
	}

	str, ok := val.(string)
	if !ok || str == "" {
		return formatCell(val)
	}

	// Try to parse as ISO8601 timestamp
	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		// Try date-only format
		t, err = time.Parse("2006-01-02", str)
		if err != nil {
			return formatCell(val)
		}
		// Date-only: return formatted date
		return t.Format("Jan 2, 2006")
	}

	// Full timestamp: show relative time for recent dates, otherwise formatted date
	now := time.Now()
	diff := now.Sub(t)

	// Future dates: just show the formatted date
	if diff < 0 {
		return t.Format("Jan 2, 2006")
	}

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("Jan 2, 2006")
	}
}

// MarkdownRenderer outputs literal Markdown syntax (portable, pipeable).
type MarkdownRenderer struct {
	width int
}

// NewMarkdownRenderer creates a renderer for literal Markdown output.
func NewMarkdownRenderer(w io.Writer) *MarkdownRenderer {
	width, _ := terminalInfo(w)
	return &MarkdownRenderer{width: width}
}

// RenderResponse renders a success response as literal Markdown.
func (r *MarkdownRenderer) RenderResponse(w io.Writer, resp *Response) error {
	var b strings.Builder

	// Summary as heading
	if resp.Summary != "" {
		b.WriteString("## " + resp.Summary + "\n")
	}

	// Notice (e.g., truncation warning)
	if resp.Notice != "" {
		b.WriteString("*" + resp.Notice + "*\n")
	}

	if resp.Summary != "" || resp.Notice != "" {
		b.WriteString("\n")
	}

	// Main data
	data := NormalizeData(resp.Data)
	r.renderData(&b, data)

	// Breadcrumbs
	if len(resp.Breadcrumbs) > 0 {
		b.WriteString("\n### Hints\n\n")
		for _, bc := range resp.Breadcrumbs {
			line := "- `" + bc.Cmd + "`"
			if bc.Description != "" {
				line += " — " + bc.Description
			}
			b.WriteString(line + "\n")
		}
	}

	// Stats (from --stats flag)
	if stats := extractStats(resp.Meta); stats != nil {
		b.WriteString("\n")
		r.renderStats(&b, stats)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// RenderError renders an error response as literal Markdown.
func (r *MarkdownRenderer) RenderError(w io.Writer, resp *ErrorResponse) error {
	var b strings.Builder

	b.WriteString("**Error:** " + resp.Error + "\n")
	if resp.Hint != "" {
		b.WriteString("\n*Hint: " + resp.Hint + "*\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func (r *MarkdownRenderer) renderData(b *strings.Builder, data any) {
	switch d := data.(type) {
	case []map[string]any:
		if len(d) == 0 {
			b.WriteString("*No results*\n")
			return
		}
		r.renderTable(b, d)

	case map[string]any:
		r.renderObject(b, d)

	case []any:
		if len(d) == 0 {
			b.WriteString("*No results*\n")
			return
		}
		if maps := toMapSlice(d); maps != nil {
			r.renderTable(b, maps)
		} else {
			r.renderList(b, d)
		}

	case string:
		b.WriteString(ansi.Strip(d) + "\n")

	case nil:
		b.WriteString("*No data*\n")

	default:
		fmt.Fprintf(b, "%v\n", ansi.Strip(fmt.Sprintf("%v", data)))
	}
}

func (r *MarkdownRenderer) renderTable(b *strings.Builder, data []map[string]any) {
	if len(data) == 0 {
		return
	}

	// Detect columns
	cols := r.detectColumns(data)
	if len(cols) == 0 {
		return
	}

	// Header row
	var headers []string
	for _, col := range cols {
		headers = append(headers, col.header)
	}
	b.WriteString("| " + strings.Join(headers, " | ") + " |\n")

	// Separator row
	var seps []string
	for range cols {
		seps = append(seps, "---")
	}
	b.WriteString("| " + strings.Join(seps, " | ") + " |\n")

	// Data rows
	for _, item := range data {
		var cells []string
		for _, col := range cols {
			cell := formatCell(item[col.key])
			// Escape pipe characters in cell content
			cell = strings.ReplaceAll(cell, "|", "\\|")
			cells = append(cells, cell)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
}

func (r *MarkdownRenderer) detectColumns(data []map[string]any) []column {
	if len(data) == 0 {
		return nil
	}

	first := data[0]
	var cols []column

	for key, val := range first {
		if skipColumns[key] {
			continue
		}

		switch val.(type) {
		case map[string]any, []map[string]any:
			continue
		case []any:
			if key != "assignees" {
				continue
			}
		}

		priority := columnPriority[key]
		if priority == 0 {
			priority = 50
		}

		cols = append(cols, column{
			key:      key,
			header:   formatHeader(key),
			priority: priority,
		})
	}

	sort.Slice(cols, func(i, j int) bool {
		return cols[i].priority < cols[j].priority
	})

	return cols
}

func (r *MarkdownRenderer) renderObject(b *strings.Builder, data map[string]any) {
	// Collect fields with priority ordering (same as styled renderer)
	var fields []renderField

	for k := range data {
		if skipObjectColumns[k] {
			continue
		}
		// Skip nested objects
		switch data[k].(type) {
		case map[string]any, []map[string]any:
			continue
		}
		priority := columnPriority[k]
		if priority == 0 {
			priority = 50
		}
		fields = append(fields, renderField{key: k, priority: priority})
	}

	// Sort by priority (lower = higher priority)
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].priority != fields[j].priority {
			return fields[i].priority < fields[j].priority
		}
		return fields[i].key < fields[j].key
	})

	if len(fields) == 0 {
		b.WriteString("*No data*\n")
		return
	}

	for _, f := range fields {
		label := formatHeader(f.key)
		value := formatDateValue(f.key, data[f.key])
		b.WriteString("- **" + label + ":** " + value + "\n")
	}
}

func (r *MarkdownRenderer) renderList(b *strings.Builder, data []any) {
	for _, item := range data {
		b.WriteString("- " + formatCell(item) + "\n")
	}
}

// renderStats renders session statistics in Markdown format.
func (r *MarkdownRenderer) renderStats(b *strings.Builder, stats map[string]any) {
	metrics := observability.SessionMetricsFromMap(stats)
	parts := metrics.FormatParts()
	if len(parts) > 0 {
		b.WriteString("*" + strings.Join(parts, " · ") + "*\n")
	}
}

// extractStats pulls stats from response meta if present.
func extractStats(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	stats, _ := meta["stats"].(map[string]any)
	return stats
}
