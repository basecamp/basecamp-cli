package presenter

import (
	"fmt"
	"strings"
	"time"
)

// FormatField formats a field value according to its FieldSpec.
func FormatField(spec FieldSpec, key string, val any) string {
	switch spec.Format {
	case "boolean":
		return formatBoolean(spec, val)
	case "date":
		return formatDate(val)
	case "relative_time":
		return formatRelativeTime(val)
	case "people":
		return formatPeople(val)
	default:
		return formatText(val)
	}
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

// formatDate formats a date string as "Jan 2, 2006".
func formatDate(val any) string {
	str, ok := val.(string)
	if !ok || str == "" {
		return ""
	}

	// Try ISO8601 full timestamp
	if t, err := time.Parse(time.RFC3339, str); err == nil {
		return t.Format("Jan 2, 2006")
	}
	// Try date-only
	if t, err := time.Parse("2006-01-02", str); err == nil {
		return t.Format("Jan 2, 2006")
	}
	return str
}

// formatRelativeTime formats a timestamp as relative time (e.g. "2 hours ago").
func formatRelativeTime(val any) string {
	str, ok := val.(string)
	if !ok || str == "" {
		return ""
	}

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
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ", ")
}

// formatText converts any value to a string representation.
func formatText(val any) string {
	switch v := val.(type) {
	case nil:
		return ""
	case string:
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
