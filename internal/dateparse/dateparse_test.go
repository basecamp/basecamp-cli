package dateparse

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	// Use a fixed reference time for testing: Wednesday, 2024-01-17
	// (Jan 15 is Monday, Jan 17 is Wednesday)
	ref := time.Date(2024, 1, 17, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		input    string
		expected string
	}{
		// Basic keywords
		{"today", "2024-01-17"},
		{"TODAY", "2024-01-17"},
		{"Tomorrow", "2024-01-18"},
		{"yesterday", "2024-01-16"},

		// Next week/month
		{"next week", "2024-01-24"},
		{"nextweek", "2024-01-24"},
		{"next month", "2024-02-17"},
		{"nextmonth", "2024-02-17"},

		// End of period
		{"eow", "2024-01-19"}, // Friday of this week
		{"end of week", "2024-01-19"},
		{"eom", "2024-01-31"}, // Last day of January
		{"end of month", "2024-01-31"},

		// Weekdays (next occurrence from Wednesday Jan 17)
		// Same day always goes to next week
		{"monday", "2024-01-22"}, // Next Monday (5 days)
		{"mon", "2024-01-22"},
		{"tuesday", "2024-01-23"},   // Next Tuesday (6 days)
		{"wednesday", "2024-01-24"}, // Same day = next week (7 days)
		{"thursday", "2024-01-18"},  // Tomorrow (1 day)
		{"friday", "2024-01-19"},    // This Friday (2 days)
		{"saturday", "2024-01-20"},  // This Saturday (3 days)
		{"sunday", "2024-01-21"},    // This Sunday (4 days)

		// Next weekday (the one after this week's, unless today IS that day)
		{"next monday", "2024-01-29"},    // Monday after this one (12 days)
		{"next wednesday", "2024-01-24"}, // Same day: just next week (7 days)
		{"next friday", "2024-01-26"},    // Friday after this one (9 days)

		// Relative days
		{"+1", "2024-01-18"},
		{"+3", "2024-01-20"},
		{"+7", "2024-01-24"},

		// In N days/weeks
		{"in 1 day", "2024-01-18"},
		{"in 3 days", "2024-01-20"},
		{"in 1 week", "2024-01-24"},
		{"in 2 weeks", "2024-01-31"},

		// YYYY-MM-DD passthrough
		{"2024-06-15", "2024-06-15"},
		{"2025-12-25", "2025-12-25"},

		// Unknown format returns as-is
		{"invalid", "invalid"},
		{"next year", "next year"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ParseFrom(tt.input, ref)
			assert.Equal(t, tt.expected, result, "ParseFrom(%q)", tt.input)
		})
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"today", true},
		{"tomorrow", true},
		{"2024-06-15", true},
		{"invalid", false},
		{"next year", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := IsValid(tt.input)
			assert.Equal(t, tt.valid, result, "IsValid(%q)", tt.input)
		})
	}
}

// TestNextWeekdaySameDay verifies that "next monday" on a Monday returns 7 days (not 14).
// This matches bash behavior where "next <day>" on that day means the coming occurrence.
func TestNextWeekdaySameDay(t *testing.T) {
	// Monday, 2024-01-15
	monday := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		input    string
		expected string
	}{
		{"monday", "2024-01-22"},      // 7 days (next Monday)
		{"next monday", "2024-01-22"}, // 7 days (same as above, not 14)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ParseFrom(tt.input, monday)
			assert.Equal(t, tt.expected, result, "ParseFrom(%q) on Monday", tt.input)
		})
	}
}

func TestEndOfMonth(t *testing.T) {
	tests := []struct {
		date     string
		expected string
	}{
		{"2024-01-15", "2024-01-31"}, // January: 31 days
		{"2024-02-15", "2024-02-29"}, // February leap year: 29 days
		{"2023-02-15", "2023-02-28"}, // February non-leap: 28 days
		{"2024-04-10", "2024-04-30"}, // April: 30 days
		{"2024-12-01", "2024-12-31"}, // December: 31 days
	}

	for _, tt := range tests {
		t.Run(tt.date, func(t *testing.T) {
			ref, _ := time.Parse("2006-01-02", tt.date)
			result := ParseFrom("eom", ref)
			assert.Equal(t, tt.expected, result, "eom for %s", tt.date)
		})
	}
}
