package summarize

import (
	"fmt"
	"strings"
)

// Segment represents a single message in a conversation.
type Segment struct {
	Author string
	Time   string
	Text   string
}

// ExtractSummary produces an extractive summary at the given zoom level.
func ExtractSummary(segments []Segment, zoom ZoomLevel) string {
	if len(segments) == 0 {
		return ""
	}

	budget := int(zoom)

	switch zoom {
	case Zoom40:
		return extractZoom40(segments, budget)
	case Zoom80:
		return extractZoom80(segments, budget)
	case Zoom200:
		return extractZoom200(segments, budget)
	default:
		return extractZoom500Plus(segments, budget)
	}
}

// Zoom40: "N messages from Author, Author2"
func extractZoom40(segments []Segment, budget int) string {
	authors := uniqueAuthors(segments)
	result := fmt.Sprintf("%d messages from %s", len(segments), strings.Join(authors, ", "))
	return truncateRunes(result, budget)
}

// Zoom80: "N messages . last: <truncated last message>"
func extractZoom80(segments []Segment, budget int) string {
	prefix := fmt.Sprintf("%d messages", len(segments))
	last := segments[len(segments)-1].Text

	// "N messages · last: " is the frame; fill remaining budget with the last message
	frame := prefix + " \u00b7 last: "
	remaining := budget - runeLen(frame)
	if remaining <= 0 {
		return truncateRunes(prefix, budget)
	}
	return frame + truncateRunes(last, remaining)
}

// Zoom200: First message + "... N more ..." + last message
func extractZoom200(segments []Segment, budget int) string {
	if len(segments) == 1 {
		return truncateRunes(formatLine(segments[0]), budget)
	}

	first := formatLine(segments[0])
	last := formatLine(segments[len(segments)-1])

	if len(segments) == 2 {
		combined := first + " " + last
		return truncateRunes(combined, budget)
	}

	middle := fmt.Sprintf(" \u2026 %d more \u2026 ", len(segments)-2)
	available := budget - runeLen(middle)
	if available <= 0 {
		return truncateRunes(first, budget)
	}

	half := available / 2
	firstTrunc := truncateRunes(first, half)
	lastTrunc := truncateRunes(last, available-runeLen(firstTrunc))
	return firstTrunc + middle + lastTrunc
}

// Zoom500+: First 2 + last 2 messages, each as "Author: Text"
func extractZoom500Plus(segments []Segment, budget int) string {
	var selected []Segment
	switch {
	case len(segments) <= 4:
		selected = segments
	default:
		selected = append(selected, segments[0], segments[1])
		selected = append(selected, segments[len(segments)-2], segments[len(segments)-1])
	}

	lines := make([]string, len(selected))
	for i, seg := range selected {
		lines[i] = formatLine(seg)
	}

	// If we picked 4 from a longer list, insert a separator
	if len(segments) > 4 {
		sep := fmt.Sprintf("\u2026 %d more \u2026", len(segments)-4)
		lines = append(lines[:2], append([]string{sep}, lines[2:]...)...)
	}

	result := strings.Join(lines, "\n")
	return truncateRunes(result, budget)
}

func formatLine(seg Segment) string {
	return seg.Author + ": " + seg.Text
}

func uniqueAuthors(segments []Segment) []string {
	seen := make(map[string]struct{})
	var authors []string
	for _, seg := range segments {
		if _, ok := seen[seg.Author]; !ok {
			seen[seg.Author] = struct{}{}
			authors = append(authors, seg.Author)
		}
	}
	return authors
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return "\u2026"
	}
	return string(runes[:maxRunes-1]) + "\u2026"
}

func runeLen(s string) int {
	return len([]rune(s))
}
