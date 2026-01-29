package presenter

import (
	"fmt"
	"strings"
	"time"
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
	case "people":
		return formatPeople(val)
	case "number":
		return formatNumber(val, locale)
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

// formatDate formats a date string using the locale's preferred date layout.
func formatDate(val any, locale Locale) string {
	str, ok := val.(string)
	if !ok || str == "" {
		return ""
	}

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
				names = append(names, name)
			}
		}
	}
	return strings.Join(names, ", ")
}

// formatText converts any value to a string representation.
// Numbers are rendered raw (no locale grouping) so IDs and other numeric
// values remain copy-paste safe. Use format: "number" for locale-aware output.
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
