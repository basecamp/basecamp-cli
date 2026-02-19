package presenter

import (
	"io"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// RenderMode controls the output format.
type RenderMode int

const (
	ModeStyled   RenderMode = iota // ANSI styled terminal output
	ModeMarkdown                   // Literal Markdown syntax
)

// Present attempts schema-aware rendering of the data.
// Returns true if a schema was found and rendering was handled.
// Returns false if no schema matched (caller should fall back to generic rendering).
func Present(w io.Writer, data any, entityHint string, mode RenderMode) bool {
	schema := Detect(data, entityHint)
	if schema == nil {
		return false
	}

	theme := tui.ResolveTheme()
	locale := DetectLocale()
	return presentWith(w, data, schema, theme, locale, mode)
}

// PresentWithTheme is like Present but accepts a theme and locale directly (for testing).
func PresentWithTheme(w io.Writer, data any, entityHint string, mode RenderMode, theme tui.Theme, locale Locale) bool {
	schema := Detect(data, entityHint)
	if schema == nil {
		return false
	}

	return presentWith(w, data, schema, theme, locale, mode)
}

func presentWith(w io.Writer, data any, schema *EntitySchema, theme tui.Theme, locale Locale, mode RenderMode) bool {
	switch mode {
	case ModeMarkdown:
		return presentMarkdown(w, data, schema, locale)
	default:
		return presentStyled(w, data, schema, theme, locale)
	}
}

func presentStyled(w io.Writer, data any, schema *EntitySchema, theme tui.Theme, locale Locale) bool {
	styles := NewStyles(theme, true)

	switch d := data.(type) {
	case map[string]any:
		if err := RenderDetail(w, schema, d, styles, locale); err != nil {
			return false
		}
		return true
	case []map[string]any:
		if len(d) == 0 {
			return false
		}
		if err := RenderList(w, schema, d, styles, locale); err != nil {
			return false
		}
		return true
	}
	return false
}

func presentMarkdown(w io.Writer, data any, schema *EntitySchema, locale Locale) bool {
	switch d := data.(type) {
	case map[string]any:
		if err := RenderDetailMarkdown(w, schema, d, locale); err != nil {
			return false
		}
		return true
	case []map[string]any:
		if len(d) == 0 {
			return false
		}
		if err := RenderListMarkdown(w, schema, d, locale); err != nil {
			return false
		}
		return true
	}
	return false
}
