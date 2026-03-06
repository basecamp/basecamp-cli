package richtext

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Hyperlink wraps text in an OSC 8 terminal hyperlink sequence.
// Returns text unchanged when url is empty.
// The URL is sanitized to strip control characters that could break
// out of the OSC 8 sequence or inject terminal commands.
func Hyperlink(text, url string) string {
	if url == "" {
		return text
	}
	url = sanitizeURL(url)
	if url == "" {
		return text
	}
	return ansi.SetHyperlink(url) + text + ansi.ResetHyperlink()
}

// sanitizeURL strips terminal control characters from a URL to prevent
// OSC 8 sequence injection. BEL (\x07) would terminate the sequence early,
// ESC (\x1b) could start new escape sequences, and the 8-bit ST (\x9c)
// can terminate OSC in some terminals. All C0, C1, and DEL characters
// have no place in a URL.
func sanitizeURL(url string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1 // strip C0 controls, DEL, and C1 controls
		}
		return r
	}, url)
	return clean
}

// reMarkdownLink matches markdown-style links [text](url).
// Handles one level of balanced parentheses in URLs (e.g., Wikipedia links).
var reMarkdownLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://(?:[^()\s]*|\([^()\s]*\))*)\)`)

// LinkifyMarkdownLinks converts markdown-style [text](url) links to OSC 8
// terminal hyperlinks where the link text is clickable.
// Use this for rendering paths that bypass glamour (e.g., campfire).
func LinkifyMarkdownLinks(text string) string {
	return reMarkdownLink.ReplaceAllStringFunc(text, func(s string) string {
		m := reMarkdownLink.FindStringSubmatch(s)
		if len(m) >= 3 {
			return Hyperlink(m[1], m[2])
		}
		return s
	})
}

// reBareURL matches bare http/https URLs not already inside an OSC 8 sequence.
// Trailing punctuation is trimmed by trimURLMatch to preserve balanced parentheses.
var reBareURL = regexp.MustCompile(`https?://[^\s\x1b\x07<>"\x00-\x1f]+`)

// trimURLMatch trims trailing punctuation from a URL match while
// preserving balanced parentheses (e.g., Wikipedia URLs).
func trimURLMatch(url string) string {
	for len(url) > 0 {
		switch url[len(url)-1] {
		case '.', ',', ';', ':', '!', '?', '\'', ']', '`':
			url = url[:len(url)-1]
		case ')':
			if strings.Count(url, "(") >= strings.Count(url, ")") {
				return url
			}
			url = url[:len(url)-1]
		default:
			return url
		}
	}
	return url
}

// LinkifyURLs wraps bare URLs in OSC 8 hyperlink sequences.
// URLs already inside an OSC 8 sequence are not double-wrapped.
func LinkifyURLs(text string) string {
	var b strings.Builder
	last := 0
	for _, loc := range reBareURL.FindAllStringIndex(text, -1) {
		start, end := loc[0], loc[1]
		// Skip if this URL is already inside an OSC 8 sequence.
		// Look for a preceding SetHyperlink that hasn't been reset.
		if isInsideHyperlink(text, start) {
			continue
		}
		// Trim trailing punctuation while preserving balanced parens.
		url := trimURLMatch(text[start:end])
		if url == "" {
			continue
		}
		end = start + len(url)
		b.WriteString(text[last:start])
		b.WriteString(Hyperlink(url, url))
		last = end
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

// isInsideHyperlink checks whether position pos in text is part of an
// existing OSC 8 hyperlink — either as the URI parameter or as the
// visible text between set and reset.
func isInsideHyperlink(text string, pos int) bool {
	prefix := text[:pos]

	// Case 1: URL is the URI param of an OSC 8 set sequence (\x1b]8;;<url>\x07)
	if strings.HasSuffix(prefix, "\x1b]8;;") {
		return true
	}

	// Case 2: URL is in the visible text between set and reset.
	// Find the last OSC 8 opener before pos.
	lastSet := strings.LastIndex(prefix, "\x1b]8;")
	if lastSet == -1 {
		return false
	}
	// Find the BEL terminator that closes the set sequence
	bel := strings.IndexByte(prefix[lastSet:], '\x07')
	if bel == -1 {
		// Unclosed set — we're inside the URI parameter
		return true
	}
	// Check if the set was a non-empty URI (not a reset)
	seq := prefix[lastSet : lastSet+bel+1]
	if seq == "\x1b]8;;\x07" {
		// This was a reset, not a set — we're outside any hyperlink
		return false
	}
	// It was a set with a URI; check that no reset follows before pos
	afterBel := lastSet + bel + 1
	resetSeq := "\x1b]8;;\x07"
	resetIdx := strings.Index(prefix[afterBel:], resetSeq)
	return resetIdx == -1
}
