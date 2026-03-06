package data

import (
	htmlpkg "html"
	"regexp"
	"strings"
	"unicode"
)

// reMention matches Basecamp @mention HTML tags.
// Mirrors the pattern from internal/richtext/richtext.go.
var reMention = regexp.MustCompile(`(?i)<bc-attachment[^>]*content-type="application/vnd\.basecamp\.mention"[^>]*>([^<]*)</bc-attachment>`)

// ExtractMentions returns the names mentioned in HTML content.
func ExtractMentions(html string) []string {
	matches := reMention.FindAllStringSubmatch(html, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 && m[1] != "" {
			names = append(names, strings.TrimSpace(m[1]))
		}
	}
	return names
}

// Tokenize splits text into lowercase word tokens, removing punctuation and stopwords.
func Tokenize(text string) []string {
	// Strip HTML tags first
	stripped := stripTags(text)
	words := strings.FieldsFunc(strings.ToLower(stripped), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	result := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) > 1 && !stopwords[w] {
			result = append(result, w)
		}
	}
	return result
}

// JaccardSimilarity computes the Jaccard similarity coefficient between two token sets.
func JaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}
	intersection := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// EndsWithQuestion returns true if the text ends with a question mark.
func EndsWithQuestion(text string) bool {
	stripped := stripTags(strings.TrimSpace(text))
	return strings.HasSuffix(strings.TrimSpace(stripped), "?")
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string {
	return tagRe.ReplaceAllString(s, " ")
}

// StripTags removes HTML tags from a string, replacing them with spaces.
func StripTags(s string) string {
	return stripTags(s)
}

// reAttachment matches bc-attachment elements (non-mention).
var reAttachment = regexp.MustCompile(`(?is)<bc-attachment[^>]*>.*?</bc-attachment>`)

// reBlockTag matches block-level HTML tags that should become line breaks.
var reBlockTag = regexp.MustCompile(`(?i)</?(div|p|br|blockquote|pre|h[1-6])\b[^>]*/?>`)

// reWhitespace collapses runs of whitespace into a single space.
var reWhitespace = regexp.MustCompile(`\s{2,}`)

// RiverText converts campfire line HTML to compact plain text for the river view.
// Replaces attachments with a paperclip indicator, strips remaining tags,
// and collapses whitespace to a single line.
func RiverText(html string) string {
	if html == "" {
		return ""
	}
	// Preserve @mentions by extracting the mention name before stripping
	s := reMention.ReplaceAllString(html, "@$1")
	// Replace non-mention attachments with indicator
	s = reAttachment.ReplaceAllString(s, " \U0001F4CE ")
	// Block tags → space (prevents words running together)
	s = reBlockTag.ReplaceAllString(s, " ")
	// Strip remaining inline tags
	s = tagRe.ReplaceAllString(s, "")
	// Decode HTML entities
	s = htmlpkg.UnescapeString(s)
	// Collapse whitespace
	s = reWhitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// CapRoomsRoundRobin selects up to maxRooms from rooms, distributing evenly
// across accounts. Ensures every account gets at least one room before any
// account gets a second.
func CapRoomsRoundRobin(rooms []BonfireRoomConfig, maxRooms int) []BonfireRoomConfig {
	if len(rooms) <= maxRooms {
		return rooms
	}
	// Group by account, preserving order within each account.
	type bucket struct {
		accountID string
		rooms     []BonfireRoomConfig
	}
	seen := make(map[string]int) // accountID -> index in buckets
	var buckets []bucket
	for _, r := range rooms {
		idx, ok := seen[r.AccountID]
		if !ok {
			idx = len(buckets)
			seen[r.AccountID] = idx
			buckets = append(buckets, bucket{accountID: r.AccountID})
		}
		buckets[idx].rooms = append(buckets[idx].rooms, r)
	}
	// Round-robin: take one from each account per pass.
	result := make([]BonfireRoomConfig, 0, maxRooms)
	for pass := 0; len(result) < maxRooms; pass++ {
		added := false
		for i := range buckets {
			if pass < len(buckets[i].rooms) {
				result = append(result, buckets[i].rooms[pass])
				added = true
				if len(result) >= maxRooms {
					break
				}
			}
		}
		if !added {
			break
		}
	}
	return result
}

// stopwords is a small set of English stopwords for Jaccard filtering.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "it": true,
	"in": true, "on": true, "at": true, "to": true, "for": true,
	"of": true, "and": true, "or": true, "but": true, "not": true,
	"with": true, "this": true, "that": true, "from": true, "by": true,
	"be": true, "as": true, "are": true, "was": true, "were": true,
	"been": true, "being": true, "have": true, "has": true, "had": true,
	"do": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true, "can": true,
	"so": true, "if": true, "me": true, "my": true, "we": true, "you": true,
	"your": true, "its": true, "i": true, "im": true,
}
