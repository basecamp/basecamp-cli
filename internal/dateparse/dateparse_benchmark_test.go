package dateparse

import (
	"testing"
	"time"
)

// Reference time for benchmarks (a Wednesday)
var benchTime = time.Date(2024, 6, 12, 10, 30, 0, 0, time.UTC)

// BenchmarkParseFrom benchmarks the main parsing function
func BenchmarkParseFrom(b *testing.B) {
	b.Run("today", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("today", benchTime)
		}
	})

	b.Run("tomorrow", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("tomorrow", benchTime)
		}
	})

	b.Run("weekday", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("monday", benchTime)
		}
	})

	b.Run("next_weekday", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("next friday", benchTime)
		}
	})

	b.Run("next_week", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("next week", benchTime)
		}
	})

	b.Run("eow", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("eow", benchTime)
		}
	})

	b.Run("eom", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("eom", benchTime)
		}
	})

	b.Run("plus_days", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("+5", benchTime)
		}
	})

	b.Run("in_days", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("in 3 days", benchTime)
		}
	})

	b.Run("in_weeks", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("in 2 weeks", benchTime)
		}
	})

	b.Run("passthrough_date", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("2024-12-31", benchTime)
		}
	})

	b.Run("unknown_format", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ParseFrom("some random text", benchTime)
		}
	})
}

// BenchmarkParseWeekday benchmarks weekday name parsing
func BenchmarkParseWeekday(b *testing.B) {
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

	for _, day := range days {
		b.Run(day, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				parseWeekday(day)
			}
		})
	}

	b.Run("abbreviated", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseWeekday("mon")
		}
	})

	b.Run("with_next", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseWeekday("next monday")
		}
	})

	b.Run("invalid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			parseWeekday("notaday")
		}
	})
}

// BenchmarkNextWeekday benchmarks weekday calculation
func BenchmarkNextWeekday(b *testing.B) {
	b.Run("same_day", func(b *testing.B) {
		// benchTime is Wednesday
		for i := 0; i < b.N; i++ {
			nextWeekday(benchTime, time.Wednesday, false)
		}
	})

	b.Run("future_day", func(b *testing.B) {
		// Friday is 2 days after Wednesday
		for i := 0; i < b.N; i++ {
			nextWeekday(benchTime, time.Friday, false)
		}
	})

	b.Run("past_day", func(b *testing.B) {
		// Monday was 2 days before Wednesday
		for i := 0; i < b.N; i++ {
			nextWeekday(benchTime, time.Monday, false)
		}
	})

	b.Run("force_next", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			nextWeekday(benchTime, time.Friday, true)
		}
	})
}

// BenchmarkEndOfMonth benchmarks end-of-month calculation
func BenchmarkEndOfMonth(b *testing.B) {
	months := []time.Time{
		time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),  // January (31 days)
		time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC),  // February (leap year, 29 days)
		time.Date(2024, 4, 15, 0, 0, 0, 0, time.UTC),  // April (30 days)
		time.Date(2024, 12, 15, 0, 0, 0, 0, time.UTC), // December (year wrap)
	}

	for _, m := range months {
		b.Run(m.Month().String(), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				endOfMonth(m)
			}
		})
	}
}

// BenchmarkIsValid benchmarks date format validation
func BenchmarkIsValid(b *testing.B) {
	b.Run("valid_keyword", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			IsValid("tomorrow")
		}
	})

	b.Run("valid_date", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			IsValid("2024-12-31")
		}
	})

	b.Run("invalid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			IsValid("not a date")
		}
	})
}

// BenchmarkFormatDate benchmarks date formatting
func BenchmarkFormatDate(b *testing.B) {
	for i := 0; i < b.N; i++ {
		formatDate(benchTime)
	}
}
