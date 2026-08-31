package workspace

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// sortName is a name broken into the pieces Basecamp sorts it by. It is a port
// of bc3's StringSortable, so a directory here comes out in the same order as
// the same directory on the web.
//
// Three things happen, in this order:
//
//   - Accents fold away, so "Ángel" files under A rather than after Z. The name
//     is decomposed and its combining marks dropped, which — unlike a straight
//     transliteration — leaves emoji intact instead of turning them into "?".
//   - A name that does not start with a letter or a digit sorts first. That is
//     what puts "@basecamp.com" and "[Test Project]" above the alphabet, where
//     both a database collation and a plain Go string compare would put them
//     somewhere else.
//   - Runs of digits compare as numbers. This is why "37signals HQ" comes before
//     "2025-12-05 Cloudflare Outage": 37 is less than 2025, however the two read
//     character by character.
type sortName struct {
	// leading is 0 for a name that starts with something other than a letter or
	// a digit, and 1 for everything else.
	leading int

	// parts alternate text and number, in the order they appear in the name.
	parts []sortPart
}

// sortPart is one run of a name: either text, or a number to compare as one.
type sortPart struct {
	text   string
	number int64
	isNum  bool
}

func newSortName(name string) sortName {
	folded := foldForSort(name)

	leading := 0
	if first, _ := utf8.DecodeRuneInString(folded); unicode.IsLetter(first) || unicode.IsDigit(first) {
		leading = 1
	}

	return sortName{leading: leading, parts: splitDigits(folded)}
}

// foldForSort strips the name down to what should be compared: trimmed,
// decomposed, stripped of combining marks, lowercased.
func foldForSort(name string) string {
	var folded strings.Builder
	for _, r := range norm.NFKD.String(strings.TrimSpace(name)) {
		if !unicode.Is(unicode.Mn, r) {
			folded.WriteRune(unicode.ToLower(r))
		}
	}
	return folded.String()
}

// splitDigits breaks a name into alternating runs of digits and everything
// else, so a run of digits can be compared as the number it is.
func splitDigits(folded string) []sortPart {
	var parts []sortPart
	var run strings.Builder
	inDigits := false

	flush := func() {
		if run.Len() == 0 {
			return
		}
		if inDigits {
			// A run too long for an int64 is not a number anyone is naming a
			// project after; compared as text it still sorts consistently.
			if number, err := strconv.ParseInt(run.String(), 10, 64); err == nil {
				parts = append(parts, sortPart{number: number, isNum: true})
				run.Reset()
				return
			}
		}
		parts = append(parts, sortPart{text: run.String()})
		run.Reset()
	}

	for _, r := range folded {
		if digit := unicode.IsDigit(r); digit != inDigits {
			flush()
			inDigits = digit
		}
		run.WriteRune(r)
	}
	flush()

	return parts
}

// before reports whether one name sorts above another.
func (s sortName) before(other sortName) bool {
	if s.leading != other.leading {
		return s.leading < other.leading
	}

	for index, part := range s.parts {
		if index >= len(other.parts) {
			return false
		}
		if cmp := part.compare(other.parts[index]); cmp != 0 {
			return cmp < 0
		}
	}
	return len(s.parts) < len(other.parts)
}

// compare orders two runs. Two numbers compare as numbers, two texts as text,
// and a number sorts above a text — which is what puts the digits between the
// symbols and the letters.
func (p sortPart) compare(other sortPart) int {
	switch {
	case p.isNum && other.isNum:
		return int64Compare(p.number, other.number)
	case p.isNum:
		return -1
	case other.isNum:
		return 1
	default:
		return strings.Compare(p.text, other.text)
	}
}

func int64Compare(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// initial is the group a name files under: # for the names that sort above the
// alphabet, 0-9 for the ones that start with a digit, and the letter itself for
// the rest. The same three the web's directory uses.
func (s sortName) initial() string {
	if s.leading == 0 || len(s.parts) == 0 {
		return "#"
	}
	if s.parts[0].isNum {
		return "0-9"
	}
	first, size := utf8.DecodeRuneInString(s.parts[0].text)
	if size == 0 {
		return "#"
	}
	return strings.ToUpper(string(first))
}
