package data

import (
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
