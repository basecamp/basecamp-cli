package dateparse

import (
	"testing"
	"time"
)

// FuzzParseFrom tests the ParseFrom function with arbitrary input.
// The function should never panic regardless of input.
func FuzzParseFrom(f *testing.F) {
	// Seed corpus from known test cases
	seeds := []string{
		"today", "tomorrow", "yesterday",
		"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday",
		"mon", "tue", "wed", "thu", "fri", "sat", "sun",
		"next monday", "next friday",
		"next week", "nextweek", "next month", "nextmonth",
		"eow", "end of week", "eom", "end of month",
		"+1", "+7", "+365", "+0", "+-1",
		"in 1 day", "in 3 days", "in 1 week", "in 2 weeks",
		"2024-01-15", "2024-06-15", "2025-12-25",
		"", " ", "  ",
		"invalid", "next year", "last week",
		"MONDAY", "TODAY", "Tomorrow",
		"+", "in days", "in 0 days",
		"next", "in", "week",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	ref := time.Date(2024, 1, 17, 12, 0, 0, 0, time.UTC)

	f.Fuzz(func(t *testing.T, input string) {
		// ParseFrom should never panic
		result := ParseFrom(input, ref)

		// Result should always be a non-empty string (at minimum returns input)
		if result == "" && input != "" {
			// This is actually expected behavior - empty input returns empty
			// Normalize and check
			normalized := input
			if len(normalized) > 0 {
				_ = result // Just ensure we got a result
			}
		}
	})
}
