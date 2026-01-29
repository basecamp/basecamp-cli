package presenter

import (
	"bytes"
	"fmt"
	"text/template"
)

// templateFuncs provides helper functions for schema templates.
var templateFuncs = template.FuncMap{
	"not": func(v any) bool {
		return !toBool(v)
	},
}

// RenderTemplate executes a Go text/template with the given data.
// Returns the rendered string, or empty string on error.
func RenderTemplate(tmpl string, data map[string]any) string {
	t, err := template.New("").Funcs(templateFuncs).Parse(tmpl)
	if err != nil {
		return ""
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return ""
	}
	return buf.String()
}

// EvalCondition evaluates a template condition (from affordance "when" field).
// Returns true if the template renders to a non-empty truthy string.
func EvalCondition(condition string, data map[string]any) bool {
	if condition == "" {
		return true // No condition means always visible
	}

	result := RenderTemplate(condition, data)
	return result == "true"
}

// RenderHeadline selects and renders the appropriate headline for the data.
func RenderHeadline(schema *EntitySchema, data map[string]any) string {
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
