package output

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/term"

	"github.com/basecamp/bcq/internal/tui"
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

	// Table styles
	Header    lipgloss.Style
	Cell      lipgloss.Style
	CellMuted lipgloss.Style
}

// NewRenderer creates a renderer with styles from the default theme.
// Styling is enabled when writing to a TTY, or when forceStyled is true.
func NewRenderer(w io.Writer, forceStyled bool) *Renderer {
	theme := tui.DefaultTheme()
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
		// Use Dark colors directly since we can't reliably detect terminal background
		// when output might be piped
		r.Summary = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Primary.Dark)).Bold(true)
		r.Muted = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted.Dark))
		r.Data = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Foreground.Dark))
		r.Error = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error.Dark)).Bold(true)
		r.Hint = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted.Dark)).Italic(true)
		r.Header = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Foreground.Dark)).Bold(true)
		r.Cell = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Foreground.Dark))
		r.CellMuted = lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted.Dark))
	} else {
		// Plain text - no styling
		r.Summary = lipgloss.NewStyle()
		r.Muted = lipgloss.NewStyle()
		r.Data = lipgloss.NewStyle()
		r.Error = lipgloss.NewStyle()
		r.Hint = lipgloss.NewStyle()
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
		b.WriteString("\n\n")
	}

	// Main data
	data := normalizeData(resp.Data)
	r.renderData(&b, data)

	// Breadcrumbs
	if len(resp.Breadcrumbs) > 0 {
		b.WriteString("\n")
		r.renderBreadcrumbs(&b, resp.Breadcrumbs)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// RenderError renders an error response to the writer.
func (r *Renderer) RenderError(w io.Writer, resp *ErrorResponse) error {
	var b strings.Builder

	b.WriteString(r.Error.Render("Error: " + resp.Error))
	b.WriteString("\n")

	if resp.Hint != "" {
		b.WriteString(r.Hint.Render("Hint: " + resp.Hint))
		b.WriteString("\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
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
		b.WriteString(r.Data.Render(d))
		b.WriteString("\n")

	case nil:
		b.WriteString(r.Muted.Render("(no data)"))
		b.WriteString("\n")

	default:
		// Fallback: format as string
		b.WriteString(r.Data.Render(fmt.Sprintf("%v", data)))
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

func (r *Renderer) renderObject(b *strings.Builder, data map[string]any) {
	// Collect keys, sorted
	var keys []string
	for k := range data {
		if skipColumns[k] {
			continue
		}
		// Skip nested objects
		switch data[k].(type) {
		case map[string]any, []map[string]any:
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		b.WriteString(r.Muted.Render("(no data)"))
		b.WriteString("\n")
		return
	}

	// Find max key length for alignment
	maxLen := 0
	for _, k := range keys {
		if len(k) > maxLen {
			maxLen = len(k)
		}
	}

	for _, key := range keys {
		label := r.Muted.Render(fmt.Sprintf("%-*s: ", maxLen, key))
		value := r.Data.Render(formatCell(data[key]))
		b.WriteString(label + value + "\n")
	}
}

func (r *Renderer) renderList(b *strings.Builder, data []any) {
	for _, item := range data {
		b.WriteString(r.Data.Render("• " + formatCell(item)))
		b.WriteString("\n")
	}
}

func (r *Renderer) renderBreadcrumbs(b *strings.Builder, crumbs []Breadcrumb) {
	b.WriteString(r.Muted.Render("Next:"))
	b.WriteString("\n")
	for _, bc := range crumbs {
		cmd := r.Muted.Render("  " + bc.Cmd)
		if bc.Description != "" {
			cmd += r.Muted.Render("  # " + bc.Description)
		}
		b.WriteString(cmd + "\n")
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
		// Truncate long strings
		if len(v) > 40 {
			return v[:37] + "..."
		}
		return v
	case bool:
		if v {
			return "yes"
		}
		return "no"
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
				items = append(items, elem)
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
					items = append(items, name)
				} else if title, ok := elem["title"].(string); ok {
					items = append(items, title)
				} else if id, ok := elem["id"]; ok {
					items = append(items, fmt.Sprintf("%v", id))
				}
			default:
				items = append(items, fmt.Sprintf("%v", item))
			}
		}
		return strings.Join(items, ", ")
	default:
		return fmt.Sprintf("%v", v)
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
		b.WriteString("## " + resp.Summary + "\n\n")
	}

	// Main data
	data := normalizeData(resp.Data)
	r.renderData(&b, data)

	// Breadcrumbs
	if len(resp.Breadcrumbs) > 0 {
		b.WriteString("\n### Next\n\n")
		for _, bc := range resp.Breadcrumbs {
			line := "- `" + bc.Cmd + "`"
			if bc.Description != "" {
				line += " — " + bc.Description
			}
			b.WriteString(line + "\n")
		}
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
		b.WriteString(d + "\n")

	case nil:
		b.WriteString("*No data*\n")

	default:
		fmt.Fprintf(b, "%v\n", data)
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
	var keys []string
	for k := range data {
		if skipColumns[k] {
			continue
		}
		switch data[k].(type) {
		case map[string]any, []map[string]any:
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		b.WriteString("*No data*\n")
		return
	}

	for _, key := range keys {
		b.WriteString("- **" + key + ":** " + formatCell(data[key]) + "\n")
	}
}

func (r *MarkdownRenderer) renderList(b *strings.Builder, data []any) {
	for _, item := range data {
		b.WriteString("- " + formatCell(item) + "\n")
	}
}
