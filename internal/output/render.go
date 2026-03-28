package output

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/richtext"
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
	return NewRendererWithTheme(w, forceStyled, tui.ResolveTheme(tui.DetectDark()))
}

// NewRendererWithTheme creates a renderer with a specific theme (for testing).
func NewRendererWithTheme(w io.Writer, forceStyled bool, theme tui.Theme) *Renderer {
	width, isTTY := terminalInfo(w)
	styled := isTTY || forceStyled

	r := &Renderer{
		width:  width,
		styled: styled,
	}

	if styled {
		// Theme colors are pre-resolved for the detected background.
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
// Uses display-cell width for proper Unicode support.
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
			wordWidth := cellWidth(word)

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

// cellWidth returns the display width of a string in terminal cells.
func cellWidth(s string) int {
	return ansi.StringWidth(s)
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

// maxSafeInt is the largest integer that float64 can represent exactly (2^53).
// Beyond this, consecutive integers have gaps, so int64(f) may silently
// round to the wrong value.
const maxSafeInt = 1 << 53

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
	"updated_at":      true,
	"dock":            true,
	"inherits_status": true,
	"url":             true,
	"app_url":         true,
}

// skipMarkdownExtra lists fields excluded from markdown tables only.
// These are internal counters or type markers that add noise to wide tables.
// The styled renderer may still show these when terminal width allows.
var skipMarkdownExtra = map[string]bool{
	"comments_count":  true,
	"boosts_count":    true,
	"position":        true,
	"attachable_sgid": true,
	"personable_type": true,
	"recording_type":  true,
}

// keepURLColumns lists _url-suffixed fields that carry meaningful data
// and should not be filtered from markdown tables.
var keepURLColumns = map[string]bool{
	"base_url":     true,
	"payload_url":  true,
	"download_url": true,
}

func skipMarkdownColumn(key string) bool {
	return skipColumns[key] ||
		skipMarkdownExtra[key] ||
		(strings.HasSuffix(key, "_url") && !keepURLColumns[key])
}

type column struct {
	key         string
	header      string
	priority    int
	muted       bool
	width       int
	containsURL bool
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
			cell := formatTableCell(col.key, item[col.key])
			if r.styled && (col.key == "title" || col.key == "name") {
				if url, ok := item["app_url"].(string); ok && url != "" {
					cell = richtext.Hyperlink(cell, url)
				}
			}
			row[i] = cell
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
			formatted := formatTableCell(cols[i].key, row[cols[i].key])
			cellWidth := lipgloss.Width(formatted)
			if cellWidth > cols[i].width {
				cols[i].width = cellWidth
			}
			if !cols[i].containsURL && isURL(formatted) {
				cols[i].containsURL = true
			}
		}
		// Cap width at 40 for long content. URL columns keep actual
		// width so column-dropping math matches what formatCell (which
		// never truncates URLs) actually renders.
		if cols[i].width > 40 && !cols[i].containsURL { //nolint:gosec // G602 false positive: i bounded by range cols
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
		// Drop the rightmost non-URL column. A dropped URL is useless;
		// an overflowing table at least keeps the link clickable.
		dropped := false
		for j := len(selected) - 1; j >= 1; j-- {
			if !selected[j].containsURL {
				selected = append(selected[:j], selected[j+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			break // only the lead column and URL columns remain
		}
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
	"boosts_url":       true,
	"completion_url":   true,
	"comment_count":    true,
	"comments_count":   true,
	"comments_url":     true,
	"position":         true,
	"attachable_sgid":  true,
	"personable_type":  true,
	"recording_type":   true,
}

func topLevelComments(data map[string]any) []map[string]any {
	comments, ok := data["comments"]
	if !ok {
		return nil
	}

	switch v := comments.(type) {
	case []map[string]any:
		return v
	case []any:
		return toMapSlice(v)
	}

	normalized := NormalizeData(comments)
	switch v := normalized.(type) {
	case []map[string]any:
		return v
	case []any:
		return toMapSlice(v)
	default:
		return nil
	}
}

// isCommentsArray reports whether data["comments"] is an array type (including
// empty arrays). Used to decide whether to suppress the raw field — non-array
// comment values (string, int) from `basecamp api get` should render as fields.
func isCommentsArray(data map[string]any) bool {
	comments, ok := data["comments"]
	if !ok {
		return false
	}
	switch comments.(type) {
	case []map[string]any, []any:
		return true
	}
	normalized := NormalizeData(comments)
	switch normalized.(type) {
	case []map[string]any, []any:
		return true
	}
	return false
}

type attachmentSection struct {
	title       string
	attachments []map[string]any
}

func attachmentMaps(val any) []map[string]any {
	switch v := val.(type) {
	case []map[string]any:
		return v
	case []any:
		return toMapSlice(v)
	}

	normalized := NormalizeData(val)
	switch v := normalized.(type) {
	case []map[string]any:
		return v
	case []any:
		return toMapSlice(v)
	default:
		return nil
	}
}

func topLevelAttachmentSections(data map[string]any) []attachmentSection {
	keys := []string{"content_attachments", "description_attachments"}
	var sections []attachmentSection
	for _, key := range keys {
		attachments := attachmentMaps(data[key])
		if len(attachments) == 0 {
			continue
		}
		sections = append(sections, attachmentSection{
			title:       formatHeader(key),
			attachments: attachments,
		})
	}
	return sections
}

func attachmentDisplayName(att map[string]any) string {
	for _, key := range []string{"filename", "caption", "path", "url", "sgid"} {
		if value, ok := att[key].(string); ok && value != "" {
			return ansi.Strip(value)
		}
	}
	return "attachment"
}

func attachmentDisplayMeta(att map[string]any) string {
	var parts []string
	if contentType, ok := att["content_type"].(string); ok && contentType != "" {
		parts = append(parts, contentType)
	}
	if filesize, ok := att["filesize"].(string); ok && filesize != "" {
		parts = append(parts, filesize+" bytes")
	}
	if path, ok := att["path"].(string); ok && path != "" {
		parts = append(parts, "saved to "+path)
	}
	if status, ok := att["download_status"].(string); ok && status != "" {
		parts = append(parts, status)
	}
	if errText, ok := att["download_error"].(string); ok && errText != "" {
		parts = append(parts, "error: "+errText)
	}
	return strings.Join(parts, " · ")
}

func commentCreatorName(comment map[string]any) string {
	if creator, ok := comment["creator"].(map[string]any); ok {
		if name, ok := creator["name"].(string); ok && name != "" {
			return ansi.Strip(name)
		}
	}
	if name, ok := comment["creator_name"].(string); ok && name != "" {
		return ansi.Strip(name)
	}
	return "Unknown"
}

func commentTimestamp(comment map[string]any) string {
	return formatDateValue("created_at", comment["created_at"])
}

func commentBody(comment map[string]any) string {
	content, _ := comment["content"].(string)
	content = ansi.Strip(content)
	if richtext.IsHTML(content) {
		content = richtext.HTMLToMarkdown(content)
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.TrimSpace(content)
}

func (r *Renderer) renderCommentsSection(b *strings.Builder, comments []map[string]any) {
	for i, comment := range comments {
		if i > 0 {
			b.WriteString("\n")
		}

		author := commentCreatorName(comment)
		timestamp := commentTimestamp(comment)
		line := r.Data.Render("- " + author)
		if timestamp != "" {
			line += r.Muted.Render(" — " + timestamp)
		}
		b.WriteString(line + "\n")

		body := commentBody(comment)
		if body == "" {
			continue
		}
		for _, bodyLine := range strings.Split(body, "\n") {
			if bodyLine == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString(r.Data.Render("  " + bodyLine))
			b.WriteString("\n")
		}
	}
}

func (r *Renderer) renderAttachmentSections(b *strings.Builder, sections []attachmentSection) {
	for i, section := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.Header.Render(section.title + ":"))
		b.WriteString("\n")
		for _, attachment := range section.attachments {
			line := r.Data.Render("- " + attachmentDisplayName(attachment))
			if meta := attachmentDisplayMeta(attachment); meta != "" {
				line += r.Muted.Render(" — " + meta)
			}
			b.WriteString(line + "\n")
		}
	}
}

func (r *Renderer) renderObject(b *strings.Builder, data map[string]any) {
	comments := topLevelComments(data)
	commentsIsArray := isCommentsArray(data)
	attachmentSections := topLevelAttachmentSections(data)

	// Collect fields with priority ordering
	var fields []renderField

	for k := range data {
		if (k == "comments" && commentsIsArray) || k == "content_attachments" || k == "description_attachments" || skipObjectColumns[k] {
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

	if len(fields) == 0 && len(attachmentSections) == 0 && len(comments) == 0 {
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

		value := formatDetailValue(f.key, data[f.key])
		// Hyperlink title/name fields when styled
		if r.styled && (f.key == "title" || f.key == "name") {
			if url, ok := data["app_url"].(string); ok && url != "" {
				value = richtext.Hyperlink(value, url)
			}
		}

		// Apply muted styling to metadata fields
		var valueStyled string
		if mutedColumns[f.key] {
			valueStyled = r.CellMuted.Render(value)
		} else {
			valueStyled = r.Data.Render(value)
		}
		b.WriteString(labelStyled + valueStyled + "\n")
	}

	if len(attachmentSections) > 0 {
		if len(fields) > 0 {
			b.WriteString("\n")
		}
		r.renderAttachmentSections(b, attachmentSections)
	}

	if len(comments) > 0 {
		if len(fields) > 0 || len(attachmentSections) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(r.Header.Render("Comments:"))
		b.WriteString("\n")
		r.renderCommentsSection(b, comments)
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
		if richtext.IsHTML(v) {
			v = richtext.HTMLToMarkdown(v)
		}
		if strings.ContainsAny(v, "\n\r") {
			v = strings.Join(strings.Fields(v), " ")
		}
		// Truncate long strings by display width.
		// HTTP(S) URLs are never truncated — a truncated URL is useless.
		if ansi.StringWidth(v) > 40 && !isURL(v) {
			return ansi.Truncate(v, 40, "...")
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
		if v == math.Trunc(v) && v >= -maxSafeInt && v <= maxSafeInt {
			return fmt.Sprintf("%d", int64(v))
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
				s := ansi.Strip(elem)
				if strings.ContainsAny(s, "\n\r") {
					s = strings.Join(strings.Fields(s), " ")
				}
				items = append(items, s)
			case json.Number:
				items = append(items, elem.String())
			case float64:
				if elem == math.Trunc(elem) && elem >= -maxSafeInt && elem <= maxSafeInt {
					items = append(items, fmt.Sprintf("%d", int64(elem)))
				} else {
					items = append(items, fmt.Sprintf("%.2f", elem))
				}
			case int, int64:
				items = append(items, fmt.Sprintf("%d", elem))
			case map[string]any:
				// Try attachment-style maps first, then generic record labels.
				// summary is checked after title because schedule entries omit title
				// from the calendar/reports API response and use summary as their
				// display name (Schedule::Entry#title delegates to summary in bc3).
				if filename, ok := elem["filename"].(string); ok && filename != "" {
					items = append(items, ansi.Strip(filename))
				} else if caption, ok := elem["caption"].(string); ok && caption != "" {
					items = append(items, ansi.Strip(caption))
				} else if name, ok := elem["name"].(string); ok && name != "" {
					items = append(items, ansi.Strip(name))
				} else if title, ok := elem["title"].(string); ok && title != "" {
					items = append(items, ansi.Strip(title))
				} else if summary, ok := elem["summary"].(string); ok && summary != "" {
					items = append(items, ansi.Strip(summary))
				} else if path, ok := elem["path"].(string); ok && path != "" {
					items = append(items, ansi.Strip(path))
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

func isURL(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) &&
		!strings.ContainsRune(s, ' ')
}

// formatTableCell formats a value for table cell display. Date columns get
// human-readable formatting via formatDateValue; everything else uses formatCell.
func formatTableCell(key string, val any) string {
	return formatDateValue(key, val)
}

// formatDetailValue formats a value for detail (single-object) display.
// Date columns get human-readable formatting via formatDateValue.
// Unlike formatCell, string values are not truncated — detail views show full content.
func formatDetailValue(key string, val any) string {
	if isDateColumn(key) {
		return formatDateValue(key, val)
	}

	switch v := val.(type) {
	case nil:
		return ""
	case string:
		v = ansi.Strip(v)
		if richtext.IsHTML(v) {
			v = richtext.HTMLToMarkdown(v)
		}
		if strings.ContainsAny(v, "\n\r") {
			v = strings.Join(strings.Fields(v), " ")
		}
		return v
	default:
		return formatCell(val)
	}
}

// formatDateValue formats date fields in a human-readable way.
// For date columns (created_at, updated_at, due_on, due_date), it converts
// ISO8601 timestamps to a more readable format.
func isDateColumn(key string) bool {
	return strings.HasSuffix(key, "_at") || strings.HasSuffix(key, "_on") || strings.HasSuffix(key, "_date")
}

func formatDateValue(key string, val any) string {
	if !isDateColumn(key) {
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
			cell := formatTableCell(col.key, item[col.key])
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
		if skipMarkdownColumn(key) {
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

func (r *MarkdownRenderer) renderCommentsSection(b *strings.Builder, comments []map[string]any) {
	for i, comment := range comments {
		if i > 0 {
			b.WriteString("\n")
		}

		line := "- **" + commentCreatorName(comment) + "**"
		if timestamp := commentTimestamp(comment); timestamp != "" {
			line += " — " + timestamp
		}
		b.WriteString(line + "\n")

		body := commentBody(comment)
		if body == "" {
			continue
		}

		b.WriteString("\n")
		for _, bodyLine := range strings.Split(body, "\n") {
			if bodyLine == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString("  " + bodyLine + "\n")
		}
	}
}

func (r *MarkdownRenderer) renderAttachmentSections(b *strings.Builder, sections []attachmentSection) {
	for i, section := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("### " + section.title + "\n\n")
		for _, attachment := range section.attachments {
			line := "- " + attachmentDisplayName(attachment)
			if meta := attachmentDisplayMeta(attachment); meta != "" {
				line += " — " + meta
			}
			b.WriteString(line + "\n")
		}
	}
}

func (r *MarkdownRenderer) renderObject(b *strings.Builder, data map[string]any) {
	comments := topLevelComments(data)
	commentsIsArray := isCommentsArray(data)
	attachmentSections := topLevelAttachmentSections(data)

	// Collect fields with priority ordering (same as styled renderer)
	var fields []renderField

	for k := range data {
		if (k == "comments" && commentsIsArray) || k == "content_attachments" || k == "description_attachments" || skipObjectColumns[k] {
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

	if len(fields) == 0 && len(attachmentSections) == 0 && len(comments) == 0 {
		b.WriteString("*No data*\n")
		return
	}

	for _, f := range fields {
		label := formatHeader(f.key)
		value := formatDetailValue(f.key, data[f.key])
		b.WriteString("- **" + label + ":** " + value + "\n")
	}

	if len(attachmentSections) > 0 {
		if len(fields) > 0 {
			b.WriteString("\n")
		}
		r.renderAttachmentSections(b, attachmentSections)
	}

	if len(comments) > 0 {
		if len(fields) > 0 || len(attachmentSections) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## Comments\n\n")
		r.renderCommentsSection(b, comments)
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
