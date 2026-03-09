package presenter

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Styles holds the lipgloss styles used by the presenter.
type Styles struct {
	Styled  bool // whether terminal styling (including OSC 8 hyperlinks) is active
	Primary lipgloss.Style
	Normal  lipgloss.Style
	Muted   lipgloss.Style
	Subtle  lipgloss.Style // for footer elements (most understated)
	Success lipgloss.Style
	Warning lipgloss.Style
	Error   lipgloss.Style
	Heading lipgloss.Style
	Label   lipgloss.Style
	Body    lipgloss.Style
}

// NewStyles creates presenter styles from a theme.
func NewStyles(theme tui.Theme, styled bool) Styles {
	if !styled {
		return Styles{
			Styled:  false,
			Primary: lipgloss.NewStyle(),
			Normal:  lipgloss.NewStyle(),
			Muted:   lipgloss.NewStyle(),
			Subtle:  lipgloss.NewStyle(),
			Success: lipgloss.NewStyle(),
			Warning: lipgloss.NewStyle(),
			Error:   lipgloss.NewStyle(),
			Heading: lipgloss.NewStyle(),
			Label:   lipgloss.NewStyle(),
			Body:    lipgloss.NewStyle(),
		}
	}

	return Styles{
		Styled:  true,
		Primary: lipgloss.NewStyle().Foreground(theme.Primary).Bold(true),
		Normal:  lipgloss.NewStyle().Foreground(theme.Foreground),
		Muted:   lipgloss.NewStyle().Foreground(theme.Muted),
		Subtle:  lipgloss.NewStyle().Foreground(theme.Border),
		Success: lipgloss.NewStyle().Foreground(theme.Success),
		Warning: lipgloss.NewStyle().Foreground(theme.Warning),
		Error:   lipgloss.NewStyle().Foreground(theme.Error),
		Heading: lipgloss.NewStyle().Foreground(theme.Muted).Bold(true),
		Label:   lipgloss.NewStyle().Foreground(theme.Muted),
		Body:    lipgloss.NewStyle().Foreground(theme.Foreground),
	}
}

// EmphasisStyle returns the style for a given emphasis string.
func (s Styles) EmphasisStyle(emphasis string) lipgloss.Style {
	switch emphasis {
	case "primary":
		return s.Primary
	case "muted":
		return s.Muted
	case "success":
		return s.Success
	case "warning":
		return s.Warning
	case "error":
		return s.Error
	default:
		return s.Normal
	}
}

// RenderDetail renders a single entity using its schema's detail view.
func RenderDetail(w io.Writer, schema *EntitySchema, data map[string]any, styles Styles, locale Locale) error {
	var b strings.Builder

	// Headline
	headline := RenderHeadline(schema, data)
	if headline != "" {
		if styles.Styled {
			headline = hyperlinkFromData(headline, data)
		}
		b.WriteString(styles.Primary.Render(headline))
		b.WriteString("\n")
	}

	// Detail sections
	if len(schema.Views.Detail.Sections) > 0 {
		for _, section := range schema.Views.Detail.Sections {
			renderDetailSection(&b, schema, section, data, styles, locale)
		}
	} else {
		// No sections defined: render all fields in role order
		renderAllFields(&b, schema, data, styles, locale)
	}

	// Affordances
	if len(schema.Actions) > 0 {
		renderAffordances(&b, schema, data, styles)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// RenderList renders a slice of entities using the schema's list view.
func RenderList(w io.Writer, schema *EntitySchema, data []map[string]any, styles Styles, locale Locale) error {
	var b strings.Builder

	columns := schema.Views.List.Columns
	if len(columns) == 0 {
		// Fall back to fields with title/detail roles, sorted for deterministic output
		var candidates []string
		for name, spec := range schema.Fields {
			if spec.Role == "title" || spec.Role == "detail" {
				candidates = append(candidates, name)
			}
		}
		sort.Strings(candidates)
		columns = candidates
	}

	if len(columns) == 0 || len(data) == 0 {
		return nil
	}

	// Pre-pass: compute max width for meta-role columns (e.g. IDs) so they
	// right-align. Non-meta columns are left as-is to avoid wasting space
	// on variable-length content like todo titles.
	widths := make([]int, len(columns))
	for i, col := range columns {
		if schema.Fields[col].Role != "meta" {
			continue
		}
		for _, item := range data {
			formatted := singleLine(FormatField(schema.Fields[col], col, item[col], locale))
			if len(formatted) > widths[i] {
				widths[i] = len(formatted)
			}
		}
	}

	// Render each row as a compact line
	for _, item := range data {
		renderListRow(&b, schema, columns, widths, item, styles, locale)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func renderDetailSection(b *strings.Builder, schema *EntitySchema, section DetailSection, data map[string]any, styles Styles, locale Locale) {
	// Section heading
	if section.Heading != "" {
		b.WriteString("\n")
		b.WriteString(styles.Heading.Render(section.Heading))
		b.WriteString("\n")
	}

	// Find max label length for alignment
	maxLen := 0
	var visibleFields []string
	for _, name := range section.Fields {
		spec := schema.Fields[name]
		val := data[name]

		// Skip collapsed empty fields
		if spec.Collapse && isEmpty(val) {
			continue
		}

		// Title role renders as the headline, not a labeled field
		if spec.Role == "title" {
			continue
		}

		// Body role renders as a text block, not labeled
		if spec.Role == "body" {
			if !isEmpty(val) {
				visibleFields = append(visibleFields, name)
			}
			continue
		}

		label := fieldLabel(name)
		if len(label) > maxLen {
			maxLen = len(label)
		}
		visibleFields = append(visibleFields, name)
	}

	for _, name := range visibleFields {
		spec := schema.Fields[name]
		val := data[name]
		formatted := FormatField(spec, name, val, locale)

		style := resolveEmphasis(spec, name, val, styles)
		// Fall back to Body style when no emphasis is specified for body fields
		if spec.Role == "body" && spec.Emphasis == "" && spec.WhenOverdue == "" {
			style = styles.Body
		}

		if spec.Role == "body" {
			b.WriteString("\n")
			b.WriteString(style.Render("  " + formatted))
			b.WriteString("\n")
			continue
		}

		// Skip empty non-collapsed fields (collapsed empties are already filtered above)
		if formatted == "" {
			continue
		}

		label := fieldLabel(name)
		b.WriteString(styles.Label.Render(fmt.Sprintf("  %-*s  ", maxLen, label)))
		b.WriteString(style.Render(formatted))
		b.WriteString("\n")
	}
}

func renderAllFields(b *strings.Builder, schema *EntitySchema, data map[string]any, styles Styles, locale Locale) {
	// Collect and sort field names for deterministic output
	fieldNames := make([]string, 0, len(schema.Fields))
	for name := range schema.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	// Order: title, body, detail, meta
	roleOrder := []string{"title", "detail", "body", "meta"}
	for _, role := range roleOrder {
		for _, name := range fieldNames {
			spec := schema.Fields[name]
			if spec.Role != role {
				continue
			}
			val := data[name]
			if spec.Collapse && isEmpty(val) {
				continue
			}
			if spec.Role == "title" {
				continue // Already rendered as headline
			}

			formatted := FormatField(spec, name, val, locale)
			if formatted == "" {
				continue
			}

			style := resolveEmphasis(spec, name, val, styles)
			if spec.Role == "body" && spec.Emphasis == "" && spec.WhenOverdue == "" {
				style = styles.Body
			}

			if spec.Role == "body" {
				b.WriteString("\n")
				b.WriteString(style.Render("  " + formatted))
				b.WriteString("\n")
			} else {
				label := fieldLabel(name)
				b.WriteString(styles.Label.Render(fmt.Sprintf("  %-12s  ", label)))
				b.WriteString(style.Render(formatted))
				b.WriteString("\n")
			}
		}
	}
}

func renderAffordances(b *strings.Builder, schema *EntitySchema, data map[string]any, styles Styles) {
	var visible []Affordance
	for _, a := range schema.Actions {
		if EvalCondition(a.When, data) {
			visible = append(visible, a)
		}
	}

	if len(visible) == 0 {
		return
	}

	// Footer separator
	b.WriteString("\n")
	b.WriteString(styles.Muted.Render("─────"))
	b.WriteString("\n")
	b.WriteString(styles.Subtle.Render("Hints:"))
	b.WriteString("\n")

	// Find max command width for alignment
	maxCmd := 0
	renderedCmds := make([]string, len(visible))
	for i, a := range visible {
		renderedCmds[i] = RenderTemplate(a.Cmd, data)
		if len(renderedCmds[i]) > maxCmd {
			maxCmd = len(renderedCmds[i])
		}
	}

	for i, a := range visible {
		cmd := renderedCmds[i]
		line := fmt.Sprintf("  %-*s  %s", maxCmd, cmd, a.Label)
		b.WriteString(styles.Subtle.Render(line))
		b.WriteString("\n")
	}
}

func renderListRow(b *strings.Builder, schema *EntitySchema, columns []string, widths []int, data map[string]any, styles Styles, locale Locale) {
	parts := make([]string, 0, len(columns))
	for i, col := range columns {
		spec := schema.Fields[col]
		val := data[col]
		formatted := singleLine(FormatField(spec, col, val, locale))

		// Right-align meta columns (IDs) when width was computed
		if widths[i] > 0 && spec.Role == "meta" {
			formatted = fmt.Sprintf("%*s", widths[i], formatted)
		}

		if styles.Styled && spec.Role == "title" {
			formatted = hyperlinkFromData(formatted, data)
		}

		style := resolveEmphasis(spec, col, val, styles)
		parts = append(parts, style.Render(formatted))
	}
	b.WriteString(strings.Join(parts, "  "))
	b.WriteString("\n")
}

// resolveEmphasis picks the right style for a field, considering conditional emphasis.
func resolveEmphasis(spec FieldSpec, _ string, val any, styles Styles) lipgloss.Style {
	// Check conditional emphasis (e.g. when_overdue applies to this field's own value)
	if spec.WhenOverdue != "" {
		if IsOverdue(val) {
			return styles.EmphasisStyle(spec.WhenOverdue)
		}
	}

	if spec.Emphasis != "" {
		return styles.EmphasisStyle(spec.Emphasis)
	}
	return styles.Normal
}

// fieldLabel converts a field key to a human label.
func fieldLabel(key string) string {
	key = strings.ReplaceAll(key, "_", " ")
	key = strings.TrimSuffix(key, " on")
	key = strings.TrimSuffix(key, " at")
	words := strings.Fields(key)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func isEmpty(val any) bool {
	if val == nil {
		return true
	}
	switch v := val.(type) {
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case []map[string]any:
		return len(v) == 0
	}
	return false
}

// hyperlinkFromData wraps text in an OSC 8 hyperlink if the data map
// contains an "app_url" key with a non-empty string value.
func hyperlinkFromData(text string, data map[string]any) string {
	if url, ok := data["app_url"].(string); ok && url != "" {
		return richtext.Hyperlink(text, url)
	}
	return text
}

// escapePipe escapes pipe characters in Markdown table cells.
func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

// =============================================================================
// Markdown Rendering
// =============================================================================

// RenderDetailMarkdown renders a single entity as Markdown.
func RenderDetailMarkdown(w io.Writer, schema *EntitySchema, data map[string]any, locale Locale) error {
	var b strings.Builder

	// Headline as bold text
	headline := RenderHeadline(schema, data)
	if headline != "" {
		b.WriteString("**" + headline + "**\n")
	}

	// Sections
	if len(schema.Views.Detail.Sections) > 0 {
		for _, section := range schema.Views.Detail.Sections {
			renderDetailSectionMarkdown(&b, schema, section, data, locale)
		}
	} else {
		renderAllFieldsMarkdown(&b, schema, data, locale)
	}

	// Affordances
	if len(schema.Actions) > 0 {
		renderAffordancesMarkdown(&b, schema, data)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// RenderListMarkdown renders a slice of entities as Markdown.
// When the schema declares a tasklist markdown style, renders as a task list
// with optional grouping. Otherwise falls back to a GFM pipe table.
// The groupByOverride, when non-empty, replaces the schema's group_by field.
func RenderListMarkdown(w io.Writer, schema *EntitySchema, data []map[string]any, locale Locale, groupByOverride string) error {
	if md := schema.Views.List.Markdown; md != nil && md.Style == "tasklist" {
		groupBy := md.GroupBy
		if groupByOverride != "" {
			groupBy = groupByOverride
		}
		return renderTaskListMarkdown(w, schema, data, locale, groupBy)
	}
	return renderTableMarkdown(w, schema, data, locale)
}

// renderTableMarkdown renders a GFM pipe table (the original default).
func renderTableMarkdown(w io.Writer, schema *EntitySchema, data []map[string]any, locale Locale) error {
	columns := schema.Views.List.Columns
	if len(columns) == 0 {
		var candidates []string
		for name, spec := range schema.Fields {
			if spec.Role == "title" || spec.Role == "detail" {
				candidates = append(candidates, name)
			}
		}
		sort.Strings(candidates)
		columns = candidates
	}
	if len(columns) == 0 || len(data) == 0 {
		_, err := io.WriteString(w, "*No results*\n")
		return err
	}

	var b strings.Builder

	// Table header
	var headers []string
	var dividers []string
	for _, col := range columns {
		headers = append(headers, fieldLabel(col))
		dividers = append(dividers, "---")
	}
	b.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	b.WriteString("| " + strings.Join(dividers, " | ") + " |\n")

	// Table rows
	for _, item := range data {
		var cells []string
		for _, col := range columns {
			spec := schema.Fields[col]
			val := item[col]
			cells = append(cells, escapePipe(singleLine(FormatField(spec, col, val, locale))))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// taskGroup holds items sharing a common group-by value.
type taskGroup struct {
	name  string
	items []map[string]any
}

// renderTaskListMarkdown renders items as a Markdown task list with optional grouping.
func renderTaskListMarkdown(w io.Writer, schema *EntitySchema, data []map[string]any, locale Locale, groupBy string) error {
	if len(data) == 0 {
		_, err := io.WriteString(w, "*No results*\n")
		return err
	}

	groups := groupByDotPath(data, groupBy)

	var b strings.Builder
	suppressHeadings := len(groups) == 1

	for i, g := range groups {
		if !suppressHeadings {
			if i > 0 {
				b.WriteString("\n")
			}
			heading := g.name
			if heading == "" {
				heading = "Other"
			}
			b.WriteString("## " + heading + "\n")
		}
		for _, item := range g.items {
			renderTaskItem(&b, schema, item, locale)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// renderTaskItem renders a single `- [ ] content (metadata)` line.
func renderTaskItem(b *strings.Builder, schema *EntitySchema, item map[string]any, locale Locale) {
	completed := toBool(item["completed"])
	checkbox := "- [ ] "
	if completed {
		checkbox = "- [x] "
	}

	content := singleLine(FormatField(schema.Fields["content"], "content", item["content"], locale))
	b.WriteString(checkbox + content)

	// Inline metadata from columns (excluding content and completed, which are structural)
	var meta []string
	for _, col := range schema.Views.List.Columns {
		if col == "content" || col == "completed" {
			continue
		}
		spec := schema.Fields[col]
		val := item[col]
		formatted := singleLine(FormatField(spec, col, val, locale))
		if formatted == "" {
			continue
		}

		switch spec.Format {
		case "date":
			if col == "due_on" {
				meta = append(meta, "due: "+formatted)
			} else {
				meta = append(meta, fieldLabel(col)+": "+formatted)
			}
		case "people":
			for _, name := range extractPeopleNames(val) {
				meta = append(meta, "@"+name)
			}
		default:
			meta = append(meta, fieldLabel(col)+": "+formatted)
		}
	}

	if len(meta) > 0 {
		b.WriteString(" (" + strings.Join(meta, ", ") + ")")
	}
	b.WriteString("\n")
}

// groupByDotPath groups items by a dot-separated field path, preserving encounter order.
// Returns a single group with an empty name when groupBy is empty or the field is absent.
func groupByDotPath(data []map[string]any, groupBy string) []taskGroup {
	if groupBy == "" {
		return []taskGroup{{items: data}}
	}

	seen := map[string]int{}
	var groups []taskGroup

	for _, item := range data {
		key := extractDotPath(item, groupBy)
		if idx, ok := seen[key]; ok {
			groups[idx].items = append(groups[idx].items, item)
		} else {
			seen[key] = len(groups)
			groups = append(groups, taskGroup{name: key, items: []map[string]any{item}})
		}
	}

	return groups
}

// extractPeopleNames extracts name strings directly from a people array value,
// avoiding comma-splitting which would break names containing commas.
func extractPeopleNames(val any) []string {
	arr, ok := val.([]any)
	if !ok {
		return nil
	}
	var names []string
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if name, ok := m["name"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// extractDotPath walks a map[string]any via dot-separated path segments
// and returns the leaf value as a string.
func extractDotPath(data map[string]any, path string) string {
	parts := strings.Split(path, ".")
	var current any = data

	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[part]
	}

	if s, ok := current.(string); ok {
		return s
	}
	if current == nil {
		return ""
	}
	return fmt.Sprintf("%v", current)
}

func renderDetailSectionMarkdown(b *strings.Builder, schema *EntitySchema, section DetailSection, data map[string]any, locale Locale) {
	if section.Heading != "" {
		b.WriteString("\n#### " + section.Heading + "\n\n")
	}

	for _, name := range section.Fields {
		spec := schema.Fields[name]
		val := data[name]

		if spec.Collapse && isEmpty(val) {
			continue
		}
		if spec.Role == "title" {
			continue
		}

		formatted := FormatField(spec, name, val, locale)

		if spec.Role == "body" {
			if formatted != "" {
				b.WriteString("\n" + formatted + "\n")
			}
			continue
		}

		if formatted == "" {
			continue
		}

		label := fieldLabel(name)
		b.WriteString("- **" + label + ":** " + formatted + "\n")
	}
}

func renderAllFieldsMarkdown(b *strings.Builder, schema *EntitySchema, data map[string]any, locale Locale) {
	fieldNames := make([]string, 0, len(schema.Fields))
	for name := range schema.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	roleOrder := []string{"title", "detail", "body", "meta"}
	for _, role := range roleOrder {
		for _, name := range fieldNames {
			spec := schema.Fields[name]
			if spec.Role != role {
				continue
			}
			val := data[name]
			if spec.Collapse && isEmpty(val) {
				continue
			}
			if spec.Role == "title" {
				continue
			}

			formatted := FormatField(spec, name, val, locale)
			if formatted == "" {
				continue
			}

			if spec.Role == "body" {
				b.WriteString("\n" + formatted + "\n")
			} else {
				label := fieldLabel(name)
				b.WriteString("- **" + label + ":** " + formatted + "\n")
			}
		}
	}
}

func renderAffordancesMarkdown(b *strings.Builder, schema *EntitySchema, data map[string]any) {
	var visible []Affordance
	for _, a := range schema.Actions {
		if EvalCondition(a.When, data) {
			visible = append(visible, a)
		}
	}
	if len(visible) == 0 {
		return
	}

	b.WriteString("\n#### Hints\n\n")
	for _, a := range visible {
		cmd := RenderTemplate(a.Cmd, data)
		b.WriteString("- `" + cmd + "` — " + a.Label + "\n")
	}
}
