package commands

import (
	"regexp"
	"strings"
	"testing"
)

// FuzzURLPathParsing tests URL path parsing patterns with arbitrary input.
// This exercises the regex patterns used in runURLParse without needing
// the full command context.
func FuzzURLPathParsing(f *testing.F) {
	// Seed corpus with known URL patterns
	seeds := []string{
		// Valid Basecamp URLs
		"https://3.basecamp.com/123/buckets/456/messages/789",
		"https://3.basecamp.com/123/buckets/456/todos/789",
		"https://3.basecamp.com/123/buckets/456/card_tables/cards/789",
		"https://3.basecamp.com/123/buckets/456/card_tables/columns/789",
		"https://3.basecamp.com/123/buckets/456/card_tables/lists/789",
		"https://3.basecamp.com/123/buckets/456/card_tables/steps/789",
		"https://3.basecamp.com/123/projects/456",
		"https://3.basecamp.com/123",
		// With fragments
		"https://3.basecamp.com/123/buckets/456/messages/789#__recording_111",
		"https://3.basecamp.com/123/buckets/456/messages/789#111",
		// Type list URLs
		"https://3.basecamp.com/123/buckets/456/messages/",
		"https://3.basecamp.com/123/buckets/456/todos",
		// Edge cases
		"",
		"not-a-url",
		"https://example.com/123/buckets/456",
		"https://3.basecamp.com/",
		"https://3.basecamp.com/abc/buckets/def/messages/ghi",
		// Potentially malicious patterns
		"https://3.basecamp.com/../../../etc/passwd",
		"https://3.basecamp.com/123/buckets/456/messages/789?query=<script>",
		"https://3.basecamp.com/123#__recording_" + strings.Repeat("9", 1000),
	}

	for _, s := range seeds {
		f.Add(s)
	}

	// Compile patterns once (same as runURLParse)
	cardPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/card_tables/cards/(\d+)`)
	columnPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/card_tables/(?:columns|lists)/(\d+)`)
	stepPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/card_tables/steps/(\d+)`)
	fullRecordingPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/([^/]+)/(\d+)`)
	typeListPattern := regexp.MustCompile(`^/(\d+)/buckets/(\d+)/([^/]+)/?$`)
	projectPattern := regexp.MustCompile(`^/(\d+)/projects/(\d+)`)
	accountOnlyPattern := regexp.MustCompile(`^/(\d+)`)
	commentPattern := regexp.MustCompile(`__recording_(\d+)`)
	numericPattern := regexp.MustCompile(`^\d+$`)

	f.Fuzz(func(t *testing.T, url string) {
		// Extract path (same logic as runURLParse)
		var fragment string
		urlPath := url
		if idx := strings.Index(url, "#"); idx != -1 {
			fragment = url[idx+1:]
			urlPath = url[:idx]
		}

		pathOnly := urlPath
		if idx := strings.Index(urlPath, "://"); idx != -1 {
			pathOnly = urlPath[idx+3:]
			if slashIdx := strings.Index(pathOnly, "/"); slashIdx != -1 {
				pathOnly = pathOnly[slashIdx:]
			}
		}

		// Exercise all patterns - none should panic
		_ = cardPattern.FindStringSubmatch(pathOnly)
		_ = columnPattern.FindStringSubmatch(pathOnly)
		_ = stepPattern.FindStringSubmatch(pathOnly)
		_ = fullRecordingPattern.FindStringSubmatch(pathOnly)
		_ = typeListPattern.FindStringSubmatch(pathOnly)
		_ = projectPattern.FindStringSubmatch(pathOnly)
		_ = accountOnlyPattern.FindStringSubmatch(pathOnly)

		// Exercise fragment patterns
		if fragment != "" {
			_ = commentPattern.FindStringSubmatch(fragment)
			_ = numericPattern.MatchString(fragment)
		}
	})
}
