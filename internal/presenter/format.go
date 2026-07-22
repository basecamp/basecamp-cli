package presenter

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// FormatField formats a field value according to its FieldSpec using the given locale.
func FormatField(spec FieldSpec, key string, val any, locale Locale) string {
	switch spec.Format {
	case "boolean":
		return formatBoolean(spec, val)
	case "date":
		return formatDate(val, locale)
	case "relative_time":
		return formatRelativeTime(val, locale)
	case "person":
		return formatPerson(val)
	case "people":
		return formatPeople(val)
	case "number":
		return formatNumber(val, locale)
	case "dock":
		return formatDock(val)
	case "steps":
		return formatSteps(val)
	default:
		return formatText(val)
	}
}

// formatSteps renders a CardStep array as a multi-line checklist.
// Each step prefixes its title with [x] for completed, [ ] for active.
func formatSteps(val any) string {
	var items []map[string]any
	switch v := val.(type) {
	case []map[string]any:
		items = v
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				items = append(items, m)
			}
		}
	}
	if len(items) == 0 {
		return ""
	}

	sort.SliceStable(items, func(i, j int) bool {
		return stepPosition(items[i]) < stepPosition(items[j])
	})

	var lines []string
	for _, m := range items {
		title, _ := m["title"].(string)
		title = richtext.SanitizeSingleLine(title)
		marker := "[ ]"
		if completed, ok := m["completed"].(bool); ok && completed {
			marker = "[x]"
		}
		lines = append(lines, fmt.Sprintf("%s %s", marker, title))
	}
	return strings.Join(lines, "\n")
}

func stepPosition(m map[string]any) int {
	switch v := m["position"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		if n, err := strconv.Atoi(v.String()); err == nil {
			return n
		}
	}
	return 1<<31 - 1
}

// formatBoolean converts a boolean to a label from the spec, or "yes"/"no".
func formatBoolean(spec FieldSpec, val any) string {
	b := toBool(val)
	key := fmt.Sprintf("%v", b)
	if label, ok := spec.Labels[key]; ok {
		return label
	}
	if b {
		return "yes"
	}
	return "no"
}

// formatDate formats a date string using the locale's preferred date layout.
func formatDate(val any, locale Locale) string {
	str, ok := val.(string)
	if !ok || str == "" {
		return ""
	}
	// Strip terminal escape sequences up front so a timestamp with embedded
	// escapes still parses, and the fallback below is already sanitized. A date
	// value renders in a single-line detail field, so collapse embedded
	// CR/newlines/tabs too — a malformed value can't break the row.
	str = richtext.SanitizeSingleLine(str)

	// Try ISO8601 full timestamp
	if t, err := time.Parse(time.RFC3339, str); err == nil {
		return locale.FormatDate(t)
	}
	// Try date-only
	if t, err := time.Parse("2006-01-02", str); err == nil {
		return locale.FormatDate(t)
	}
	return str
}

// formatRelativeTime formats a timestamp as relative time (e.g. "2 hours ago").
// Falls back to the locale's date format for dates older than a week.
func formatRelativeTime(val any, locale Locale) string {
	str, ok := val.(string)
	if !ok || str == "" {
		return ""
	}
	// Strip terminal escape sequences up front so a timestamp with embedded
	// escapes still parses, and the fallback below is already sanitized. A
	// relative-time value renders in a single-line detail field, so collapse
	// embedded CR/newlines/tabs too — a malformed value can't break the row.
	str = richtext.SanitizeSingleLine(str)

	t, err := time.Parse(time.RFC3339, str)
	if err != nil {
		// Try date-only
		t, err = time.Parse("2006-01-02", str)
		if err != nil {
			return str
		}
	}

	now := time.Now()
	diff := now.Sub(t)

	if diff < 0 {
		return locale.FormatDate(t)
	}

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return relativeTimeFormat(int(diff.Minutes()), "minute")
	case diff < 24*time.Hour:
		return relativeTimeFormat(int(diff.Hours()), "hour")
	case diff < 7*24*time.Hour:
		return relativeTimeFormat(int(diff.Hours()/24), "day")
	default:
		return locale.FormatDate(t)
	}
}

// formatNumber formats a numeric value with locale-appropriate separators.
func formatNumber(val any, locale Locale) string {
	switch v := val.(type) {
	case float64:
		return locale.FormatNumber(v)
	case int:
		return locale.FormatNumber(float64(v))
	case int64:
		return locale.FormatNumber(float64(v))
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatPeople formats an array of people (maps with "name" field) as comma-separated names.
func formatPeople(val any) string {
	arr, ok := val.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}

	var names []string
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			if name, ok := m["name"].(string); ok {
				// Names render in a single-line detail row (e.g. todo.assignees),
				// so collapse embedded CR/newlines/tabs to spaces rather than
				// letting a crafted name break the row or glue words together.
				if stripped := richtext.SanitizeSingleLine(name); stripped != "" {
					names = append(names, stripped)
				}
			}
		}
	}
	return strings.Join(names, ", ")
}

// dockItem holds parsed dock tool data for rendering.
type dockItem struct {
	id       string
	title    string
	name     string
	disabled bool
}

// parseDockItems extracts and sorts dock items from the raw value.
func parseDockItems(val any) []dockItem {
	var items []map[string]any
	switch v := val.(type) {
	case []map[string]any:
		items = v
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				items = append(items, m)
			}
		}
	}
	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		return dockPosition(items[i]) < dockPosition(items[j])
	})

	result := make([]dockItem, len(items))
	for i, m := range items {
		title, _ := m["title"].(string)
		// A dock title/name occupies one line of the dock listing, so collapse
		// embedded CR/newlines/tabs to spaces to keep each row intact.
		title = richtext.SanitizeSingleLine(title)
		name, _ := m["name"].(string)
		name = richtext.SanitizeSingleLine(name)
		if title == "" {
			title = name
		}
		disabled := false
		if e, ok := m["enabled"].(bool); ok && !e {
			disabled = true
		}
		result[i] = dockItem{
			id:       formatText(m["id"]),
			title:    title,
			name:     name,
			disabled: disabled,
		}
	}
	return result
}

// formatDock formats a project dock (array of tool maps) as a multi-line listing.
// Items are sorted by their position field to match the order configured in the web UI.
func formatDock(val any) string {
	items := parseDockItems(val)
	if len(items) == 0 {
		return ""
	}

	maxIDWidth := 0
	for _, item := range items {
		if len(item.id) > maxIDWidth {
			maxIDWidth = len(item.id)
		}
	}

	var lines []string
	for _, item := range items {
		line := fmt.Sprintf("%*s  %s", maxIDWidth, item.id, item.title)
		if item.name != "" && item.name != item.title {
			line += fmt.Sprintf(" (%s)", item.name)
		}
		if item.disabled {
			line += " [disabled]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// formatDockStyled formats a dock with the title rendered in the primary style.
func formatDockStyled(val any, styles Styles) string {
	items := parseDockItems(val)
	if len(items) == 0 {
		return ""
	}

	maxIDWidth := 0
	for _, item := range items {
		if len(item.id) > maxIDWidth {
			maxIDWidth = len(item.id)
		}
	}

	var lines []string
	for _, item := range items {
		line := fmt.Sprintf("%*s  %s", maxIDWidth, item.id, styles.Primary.Render(item.title))
		if item.name != "" && item.name != item.title {
			line += fmt.Sprintf(" (%s)", item.name)
		}
		if item.disabled {
			line += " [disabled]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// dockPosition extracts the position from a dock item map.
// Items without a position sort to the end (max int).
func dockPosition(m map[string]any) int {
	switch v := m["position"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := strconv.Atoi(v.String()); err == nil {
			return n
		}
	}
	return 1<<31 - 1
}

// formatPerson formats a single person object (map with "name" field).
func formatPerson(val any) string {
	if m, ok := val.(map[string]any); ok {
		if name, ok := m["name"].(string); ok {
			// A person name is a single key/value detail row value, so collapse
			// embedded CR/newlines/tabs to spaces to keep the row on one line.
			return richtext.SanitizeSingleLine(name)
		}
	}
	return ""
}

// singleLine collapses multiline text into a single line by joining all
// non-empty lines with spaces. Leading/trailing whitespace is trimmed.
func singleLine(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return strings.TrimSpace(s)
	}
	// Normalize \r\n and bare \r to \n before splitting.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var parts []string
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ")
}

// formatText converts any value to a string representation.
// Numbers are rendered raw (no locale grouping) so IDs and other numeric
// values remain copy-paste safe. Use format: "number" for locale-aware output.
func formatText(val any) string {
	switch v := val.(type) {
	case nil:
		return ""
	case string:
		// Strip terminal escape sequences from API-controlled strings before
		// they reach a styled/markdown sink (terminal injection defense).
		v = richtext.SanitizeTerminal(v)
		if richtext.IsHTML(v) {
			// Defense-in-depth: strip the generated markdown too before it can
			// reach a styled/markdown sink. The input is already stripped above
			// and HTMLToMarkdown never emits ESC, so this is belt-and-suspenders.
			return richtext.SanitizeTerminal(richtext.HTMLToMarkdown(v))
		}
		return v
	case bool:
		if v {
			return "yes"
		}
		return "no"
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case []any:
		var items []string
		for _, item := range v {
			items = append(items, formatText(item))
		}
		return strings.Join(items, ", ")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// toBool converts various types to bool.
func toBool(val any) bool {
	switch v := val.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	case float64:
		return v != 0
	default:
		return false
	}
}

// IsOverdue checks if a date value is before the start of today in local time.
// Handles both date-only ("2006-01-02") and RFC3339 timestamps.
func IsOverdue(val any) bool {
	str, ok := val.(string)
	if !ok || str == "" {
		return false
	}

	now := time.Now()
	todayLocal := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// Try RFC3339 first — compare in local time
	if t, err := time.Parse(time.RFC3339, str); err == nil {
		return t.In(now.Location()).Before(todayLocal)
	}
	// Date-only values have no timezone; parse in local timezone
	if t, err := time.ParseInLocation("2006-01-02", str, now.Location()); err == nil {
		return t.Before(todayLocal)
	}
	return false
}
