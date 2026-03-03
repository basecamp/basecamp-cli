package presenter

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"text/template"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// templateFuncs provides helper functions for schema templates.
var templateFuncs = template.FuncMap{
	"not": func(v any) bool {
		return !toBool(v)
	},
}

// RenderTemplate executes a Go text/template with the given data.
// Numeric values that are integer-like floats (common from JSON) are
// coerced to int64 before rendering to avoid scientific notation in output.
// Returns a placeholder on parse/execute errors to make failures visible.
func RenderTemplate(tmpl string, data map[string]any) string {
	t, err := template.New("").Funcs(templateFuncs).Parse(tmpl)
	if err != nil {
		return "<template error>"
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, sanitizeNumericValues(data)); err != nil {
		return "<template error>"
	}
	return buf.String()
}

// EvalCondition evaluates a template condition (from affordance "when" field).
// Returns true if the template renders to exactly "true" (as produced by
// Go's text/template for boolean true values and the {{not}} helper).
// Empty conditions are always true (unconditional visibility).
func EvalCondition(condition string, data map[string]any) bool {
	if condition == "" {
		return true // No condition means always visible
	}

	result := RenderTemplate(condition, data)
	return result == "true"
}

// sanitizeNumericValues returns a shallow copy of data with integer-like
// float64 values converted to int64. This prevents text/template from
// rendering large IDs as scientific notation (e.g. 1.23457e+08).
func sanitizeNumericValues(data map[string]any) map[string]any {
	out := make(map[string]any, len(data))
	for k, v := range data {
		if f, ok := v.(float64); ok && f == math.Trunc(f) && !math.IsInf(f, 0) && !math.IsNaN(f) {
			out[k] = int64(f)
		} else {
			out[k] = v
		}
	}
	return out
}

// RenderHeadline selects and renders the appropriate headline for the data.
// If the raw headline contains HTML, it is converted to markdown and collapsed
// to a single line so it stays compact in list and detail views.
// Inline emphasis is stripped because headlines are always rendered in a
// bold/primary context (lipgloss or **...** wrapper), so nested markers
// would produce visual noise like ****word****.
func RenderHeadline(schema *EntitySchema, data map[string]any) string {
	raw := renderHeadlineRaw(schema, data)
	if richtext.IsHTML(raw) {
		md := singleLine(richtext.HTMLToMarkdown(raw))
		return strings.ReplaceAll(md, "**", "")
	}
	return raw
}

func renderHeadlineRaw(schema *EntitySchema, data map[string]any) string {
	if schema.Headline == nil {
		// Fall back to identity label
		if label := schema.Identity.Label; label != "" {
			if v, ok := data[label]; ok {
				return fmt.Sprintf("%v", v)
			}
		}
		return ""
	}

	// Check conditional headlines (e.g. "completed")
	for key, spec := range schema.Headline {
		if key == "default" {
			continue
		}
		// The key corresponds to a boolean data field
		if toBool(data[key]) {
			if rendered := RenderTemplate(spec.Template, data); rendered != "" {
				return rendered
			}
		}
	}

	// Fall back to default headline
	if spec, ok := schema.Headline["default"]; ok {
		return RenderTemplate(spec.Template, data)
	}

	return ""
}
