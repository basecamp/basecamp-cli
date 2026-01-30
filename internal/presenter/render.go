package presenter

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/bcq/internal/tui"
)

// Styles holds the lipgloss styles used by the presenter.
type Styles struct {
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
		Primary: lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Primary.Dark)).Bold(true),
		Normal:  lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Foreground.Dark)),
		Muted:   lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted.Dark)),
		Subtle:  lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Border.Dark)),
		Success: lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Success.Dark)),
		Warning: lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Warning.Dark)),
		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Error.Dark)),
		Heading: lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted.Dark)).Bold(true),
		Label:   lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Muted.Dark)),
		Body:    lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Foreground.Dark)),
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

	// Render each row as a compact line
	for _, item := range data {
		renderListRow(&b, schema, columns, item, styles, locale)
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
	b.WriteString(styles.Subtle.Render("Next:"))
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

func renderListRow(b *strings.Builder, schema *EntitySchema, columns []string, data map[string]any, styles Styles, locale Locale) {
	parts := make([]string, 0, len(columns))
	for _, col := range columns {
		spec := schema.Fields[col]
		val := data[col]
		formatted := FormatField(spec, col, val, locale)

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

// RenderListMarkdown renders a slice of entities as a Markdown table.
func RenderListMarkdown(w io.Writer, schema *EntitySchema, data []map[string]any, locale Locale) error {
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
		return nil
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
			cells = append(cells, escapePipe(FormatField(spec, col, val, locale)))
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}

	_, err := io.WriteString(w, b.String())
	return err
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

	b.WriteString("\n#### Next\n\n")
	for _, a := range visible {
		cmd := RenderTemplate(a.Cmd, data)
		b.WriteString("- `" + cmd + "` — " + a.Label + "\n")
	}
}
