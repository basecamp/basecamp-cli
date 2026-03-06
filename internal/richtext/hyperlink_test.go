package richtext

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func TestHyperlink(t *testing.T) {
	got := Hyperlink("click", "https://x.com")
	assert.Equal(t, "\x1b]8;;https://x.com\x07click\x1b]8;;\x07", got)
}

func TestHyperlinkEmptyURL(t *testing.T) {
	assert.Equal(t, "text", Hyperlink("text", ""))
}

func TestHyperlinkStripsCleanly(t *testing.T) {
	got := Hyperlink("click me", "https://example.com")
	assert.Equal(t, "click me", ansi.Strip(got))
}

func TestHyperlinkSanitizesControlChars(t *testing.T) {
	// BEL in URL would terminate OSC 8 early
	got := Hyperlink("click", "https://evil.com\x07injected")
	assert.NotContains(t, got, "\x07injected\x1b",
		"BEL in URL must be stripped to prevent sequence breakout")
	assert.Contains(t, got, "https://evil.cominjected")
}

func TestHyperlinkSanitizesESC(t *testing.T) {
	// ESC in URL could start new escape sequences
	got := Hyperlink("click", "https://evil.com\x1b[31mred")
	assert.NotContains(t, got, "\x1b[31m",
		"ESC in URL must be stripped to prevent injection")
}

func TestHyperlinkSanitizesAllControlChars(t *testing.T) {
	got := Hyperlink("click", "https://example.com/\x00\x01\x0a\x0d\x1fpath")
	assert.Contains(t, got, "https://example.com/path",
		"all C0 control characters should be stripped")
}

func TestHyperlinkAllControlsYieldEmpty(t *testing.T) {
	// URL that becomes empty after sanitization
	got := Hyperlink("text", "\x07\x1b\x00")
	assert.Equal(t, "text", got, "URL that sanitizes to empty should return plain text")
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean URL", "https://example.com", "https://example.com"},
		{"strips BEL", "https://x.com\x07rest", "https://x.comrest"},
		{"strips ESC", "https://x.com\x1b[0m", "https://x.com[0m"},
		{"strips null", "https://x.com\x00y", "https://x.comy"},
		{"strips newline", "https://x.com\ninjected", "https://x.cominjected"},
		{"strips DEL", "https://x.com\x7fpath", "https://x.compath"},
		{"preserves unicode", "https://example.com/résumé", "https://example.com/résumé"},
		{"all controls", "\x07\x1b\x00", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeURL(tt.input))
		})
	}
}

func TestLinkifyMarkdownLinks(t *testing.T) {
	input := "Check [this link](https://example.com/path) for details"
	got := LinkifyMarkdownLinks(input)

	assert.NotContains(t, got, "[this link]")
	assert.Contains(t, got, "this link")
	assert.Contains(t, got, "\x1b]8;;https://example.com/path\x07")
	assert.Equal(t, "Check this link for details", ansi.Strip(got))
}

func TestLinkifyMarkdownLinksMultiple(t *testing.T) {
	input := "Visit [A](https://a.com) and [B](https://b.com)"
	got := LinkifyMarkdownLinks(input)

	assert.Contains(t, got, "\x1b]8;;https://a.com\x07A\x1b]8;;\x07")
	assert.Contains(t, got, "\x1b]8;;https://b.com\x07B\x1b]8;;\x07")
}

func TestLinkifyMarkdownLinksNoMatch(t *testing.T) {
	input := "No links here, just plain text"
	assert.Equal(t, input, LinkifyMarkdownLinks(input))
}

func TestLinkifyURLs(t *testing.T) {
	input := "Visit https://example.com for info"
	got := LinkifyURLs(input)

	assert.Contains(t, got, "\x1b]8;;https://example.com\x07https://example.com\x1b]8;;\x07")
	assert.Equal(t, "Visit https://example.com for info", ansi.Strip(got))
}

func TestLinkifyURLsMultiple(t *testing.T) {
	input := "See https://a.com and https://b.com/path"
	got := LinkifyURLs(input)

	assert.Contains(t, got, "\x1b]8;;https://a.com\x07")
	assert.Contains(t, got, "\x1b]8;;https://b.com/path\x07")
}

func TestLinkifyURLsNoDoubleWrap(t *testing.T) {
	url := "https://example.com"
	already := Hyperlink("click", url)
	input := "Before " + already + " after"
	got := LinkifyURLs(input)

	// Should not add a second layer of OSC 8 around the URL
	assert.Equal(t, input, got)
}

func TestLinkifyURLsTrailingPunctuation(t *testing.T) {
	tests := []struct {
		input   string
		wantURL string
	}{
		{"See https://example.com.", "https://example.com"},
		{"(https://example.com)", "https://example.com"},
		{"Visit https://example.com, then", "https://example.com"},
		{"Go to https://example.com!", "https://example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := LinkifyURLs(tt.input)
			assert.Contains(t, got, "\x1b]8;;"+tt.wantURL+"\x07")
		})
	}
}

func TestLinkifyURLsWithQueryAndFragment(t *testing.T) {
	input := "See https://example.com/path?q=1&r=2#section for details"
	got := LinkifyURLs(input)
	assert.Contains(t, got, "\x1b]8;;https://example.com/path?q=1&r=2#section\x07")
}

func TestLinkifyURLsHTTP(t *testing.T) {
	input := "Visit http://example.com for info"
	got := LinkifyURLs(input)
	assert.Contains(t, got, "\x1b]8;;http://example.com\x07")
}

func TestLinkifyURLsNoURLs(t *testing.T) {
	input := "No URLs here"
	assert.Equal(t, input, LinkifyURLs(input))
}

func TestHTMLToMarkdownThenLinkifyRoundTrip(t *testing.T) {
	html := `<p>Check <a href="https://example.com">this link</a> and visit https://bare.com</p>`
	md := HTMLToMarkdown(html)

	// HTMLToMarkdown produces standard markdown links
	assert.Contains(t, md, "[this link](https://example.com)")

	// LinkifyMarkdownLinks converts them to OSC 8
	linked := LinkifyMarkdownLinks(md)
	assert.Contains(t, linked, "\x1b]8;;https://example.com\x07this link\x1b]8;;\x07")
	// Bare URL preserved for LinkifyURLs
	assert.Contains(t, linked, "https://bare.com")

	// LinkifyURLs wraps the bare URL without double-wrapping the existing hyperlink
	final := LinkifyURLs(linked)
	assert.Contains(t, final, "\x1b]8;;https://bare.com\x07")
	// example.com link should appear exactly once as a set sequence
	assert.Equal(t, 1, strings.Count(final, "\x1b]8;;https://example.com\x07"),
		"should have exactly one set for example.com link (no double-wrap)")
}
