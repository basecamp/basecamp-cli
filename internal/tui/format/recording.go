// Package format provides formatting helpers for TUI components.
package format

import (
	"fmt"
	"strings"
	"time"
)

// Recording formats a recording for picker display.
type Recording struct {
	ID        int64
	Type      string
	Title     string
	Content   string
	Creator   string
	CreatedAt time.Time
}

// ToPickerTitle returns a formatted title for picker display.
func (r Recording) ToPickerTitle() string {
	title := r.Title
	if title == "" {
		title = truncate(StripHTML(r.Content), 50)
	}
	if title == "" {
		title = fmt.Sprintf("%s #%d", r.Type, r.ID)
	}

	// Add type indicator
	typeIcon := RecordingTypeIcon(r.Type)
	if typeIcon != "" {
		title = typeIcon + " " + title
	}

	return title
}

// ToPickerDescription returns a formatted description for picker display.
func (r Recording) ToPickerDescription() string {
	parts := []string{fmt.Sprintf("#%d", r.ID)}

	if r.Creator != "" {
		parts = append(parts, "by "+r.Creator)
	}

	if !r.CreatedAt.IsZero() {
		parts = append(parts, RelativeTime(r.CreatedAt))
	}

	return strings.Join(parts, " - ")
}

// RecordingTypeIcon returns an icon for the recording type.
func RecordingTypeIcon(recordingType string) string {
	switch strings.ToLower(recordingType) {
	case "todo":
		return "[]"
	case "message":
		return "M"
	case "comment":
		return "#"
	case "document":
		return "D"
	case "upload":
		return "^"
	case "kanban::card":
		return "K"
	case "question":
		return "?"
	case "question::answer":
		return "A"
	case "schedule::entry":
		return "S"
	default:
		return ""
	}
}

// RecordingTypeName returns a human-readable name for recording types.
func RecordingTypeName(recordingType string) string {
	switch strings.ToLower(recordingType) {
	case "todo":
		return "Todo"
	case "message":
		return "Message"
	case "comment":
		return "Comment"
	case "document":
		return "Document"
	case "upload":
		return "Upload"
	case "kanban::card":
		return "Card"
	case "question":
		return "Question"
	case "question::answer":
		return "Answer"
	case "schedule::entry":
		return "Event"
	default:
		return recordingType
	}
}

// truncate shortens a string to the specified length, adding ellipsis if needed.
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-1]) + "…"
}

// StripHTML removes HTML tags from a string and normalizes whitespace.
func StripHTML(s string) string {
	var result strings.Builder
	inTag := false
	for _, c := range s {
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(c)
		}
	}
	// Normalize whitespace
	return strings.Join(strings.Fields(result.String()), " ")
}

// RelativeTime formats a time as a relative duration (e.g., "2h ago", "3d ago").
func RelativeTime(t time.Time) string {
	diff := time.Since(t)

	if diff < time.Minute {
		return "just now"
	}
	if diff < time.Hour {
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	}
	if diff < 24*time.Hour {
		hours := int(diff.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	}
	if diff < 7*24*time.Hour {
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
	if diff < 30*24*time.Hour {
		weeks := int(diff.Hours() / 24 / 7)
		if weeks == 1 {
			return "1w ago"
		}
		return fmt.Sprintf("%dw ago", weeks)
	}
	if diff < 365*24*time.Hour {
		months := int(diff.Hours() / 24 / 30)
		if months == 1 {
			return "1mo ago"
		}
		return fmt.Sprintf("%dmo ago", months)
	}

	years := int(diff.Hours() / 24 / 365)
	if years == 1 {
		return "1y ago"
	}
	return fmt.Sprintf("%dy ago", years)
}
