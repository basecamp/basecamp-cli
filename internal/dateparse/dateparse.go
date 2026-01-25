// Package dateparse provides natural language date parsing.
package dateparse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parse parses a natural language date string and returns a date in YYYY-MM-DD format.
// Supported formats:
//   - today, tomorrow, yesterday
//   - monday, tuesday, ... (next occurrence, same day = next week)
//   - next monday, next tuesday, ... (at least 7 days from now)
//   - next week, next month
//   - eow (end of week - Friday)
//   - eom (end of month)
//   - +N (N days from now)
//   - in N days, in N weeks
//   - YYYY-MM-DD (passthrough)
func Parse(input string) string {
	return ParseFrom(input, time.Now())
}

// ParseFrom parses a date relative to the given reference time.
// This is useful for testing and for parsing relative to a specific date.
func ParseFrom(input string, now time.Time) string {
	input = strings.ToLower(strings.TrimSpace(input))

	switch input {
	case "today":
		return formatDate(now)
	case "tomorrow":
		return formatDate(now.AddDate(0, 0, 1))
	case "yesterday":
		return formatDate(now.AddDate(0, 0, -1))
	case "next week", "nextweek":
		return formatDate(now.AddDate(0, 0, 7))
	case "next month", "nextmonth":
		return formatDate(now.AddDate(0, 1, 0))
	case "end of week", "eow":
		return formatDate(nextWeekday(now, time.Friday, false))
	case "end of month", "eom":
		return formatDate(endOfMonth(now))
	}

	// Weekday names
	if day, ok := parseWeekday(input); ok {
		next := strings.HasPrefix(input, "next ")
		return formatDate(nextWeekday(now, day, next))
	}

	// +N days format
	if strings.HasPrefix(input, "+") {
		if days, err := strconv.Atoi(input[1:]); err == nil {
			return formatDate(now.AddDate(0, 0, days))
		}
	}

	// "in N days" format
	if match := inDaysPattern.FindStringSubmatch(input); match != nil {
		if days, err := strconv.Atoi(match[1]); err == nil {
			return formatDate(now.AddDate(0, 0, days))
		}
	}

	// "in N weeks" format
	if match := inWeeksPattern.FindStringSubmatch(input); match != nil {
		if weeks, err := strconv.Atoi(match[1]); err == nil {
			return formatDate(now.AddDate(0, 0, weeks*7))
		}
	}

	// YYYY-MM-DD passthrough
	if datePattern.MatchString(input) {
		return input
	}

	// Return as-is if not recognized
	return input
}

var (
	datePattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	inDaysPattern  = regexp.MustCompile(`^in (\d+) days?$`)
	inWeeksPattern = regexp.MustCompile(`^in (\d+) weeks?$`)
)

func formatDate(t time.Time) string {
	return t.Format("2006-01-02")
}

func parseWeekday(input string) (time.Weekday, bool) {
	// Remove "next " prefix if present
	input = strings.TrimPrefix(input, "next ")

	switch input {
	case "sunday", "sun":
		return time.Sunday, true
	case "monday", "mon":
		return time.Monday, true
	case "tuesday", "tue":
		return time.Tuesday, true
	case "wednesday", "wed":
		return time.Wednesday, true
	case "thursday", "thu":
		return time.Thursday, true
	case "friday", "fri":
		return time.Friday, true
	case "saturday", "sat":
		return time.Saturday, true
	}
	return 0, false
}

// nextWeekday returns the next occurrence of the given weekday.
// If forceNext is true ("next monday"), it returns the Monday after this week's.
// If forceNext is false ("monday"), it returns the nearest future occurrence.
// Special case: if today IS the target weekday, both return 7 days (next week).
func nextWeekday(now time.Time, target time.Weekday, forceNext bool) time.Time {
	current := now.Weekday()
	daysUntil := int(target - current)
	sameDay := daysUntil == 0

	if daysUntil <= 0 {
		// Same day or past day this week = go to next week
		daysUntil += 7
	}

	if forceNext && !sameDay {
		// "next monday" means the one after this week's Monday
		// But if today IS Monday, "next monday" = this coming Monday (7 days)
		daysUntil += 7
	}

	return now.AddDate(0, 0, daysUntil)
}

// endOfMonth returns the last day of the current month.
func endOfMonth(now time.Time) time.Time {
	// Go to first day of next month, then subtract one day
	year, month, _ := now.Date()
	firstOfNextMonth := time.Date(year, month+1, 1, 0, 0, 0, 0, now.Location())
	return firstOfNextMonth.AddDate(0, 0, -1)
}

// IsValid returns true if the input is a recognized date format.
func IsValid(input string) bool {
	result := Parse(input)
	// If the result matches the YYYY-MM-DD pattern, it was successfully parsed
	return datePattern.MatchString(result)
}

// MustParse parses a date and panics if it fails.
// Use this only for known-good inputs like constants.
func MustParse(input string) string {
	result := Parse(input)
	if !datePattern.MatchString(result) {
		panic("dateparse: invalid date: " + input)
	}
	return result
}
