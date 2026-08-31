package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Terminals disagree about exactly the clusters the width rules leave undefined —
// a spacing combining mark like a Devanagari matra or a Thai vowel sign, a
// text-default symbol forced to emoji by a variation selector, a joined emoji on
// a terminal that does not join.
//
// The disagreement is not cosmetic: the terminal advances the cursor by what it
// drew, so every cell to the right of such a glyph shifts and every aligned row
// it sits on shears. No width table is right everywhere, so these widths are
// asked of the terminal itself at startup — see CalibrateWidths — and kept here.
// Without an answer the guess errs wide: over-allocating shows as a one-cell gap
// beside the glyph, under-allocating shears the row.
type clusterWidths struct {
	spacingMark int  // cells one spacing combining mark adds to its base
	vs16        int  // cells for a text-default base forced to emoji presentation
	flagPair    int  // cells for a regional-indicator pair
	skinTone    int  // cells for an emoji carrying a skin-tone modifier
	zwjJoined   bool // whether a ZWJ sequence draws as one glyph
}

var widths = clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}

const (
	zwj  = '‍'
	vs16 = '️'
)

// DisplayWidth is the number of terminal cells s occupies, by the calibrated
// widths. Styling is not counted and a multi-line string measures its widest
// line — the same contract lipgloss.Width has, so this drops in wherever that
// was, and answers for the clusters lipgloss can only guess at.
func DisplayWidth(s string) int {
	widest := 0
	for line := range strings.SplitSeq(s, "\n") {
		widest = max(widest, lineWidth(line))
	}
	return widest
}

func lineWidth(line string) int {
	total := 0
	for rest := ansi.Strip(line); rest != ""; {
		cluster, _ := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		if cluster == "" {
			break
		}
		total += clusterCells(cluster)
		rest = rest[len(cluster):]
	}
	return total
}

// FirstCluster returns the leading grapheme cluster of s and the cells it
// occupies, which is what lets a cut land between glyphs rather than inside one.
func FirstCluster(s string) (string, int) {
	cluster, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	return cluster, clusterCells(cluster)
}

// FitGraphemes keeps whole grapheme clusters from the front of s until the next
// one would not fit in width cells, so a cut never lands inside an emoji
// sequence or between a base letter and its combining marks.
func FitGraphemes(s string, width int) string {
	var b strings.Builder
	for s != "" {
		cluster, clusterWidth := FirstCluster(s)
		if cluster == "" || clusterWidth > width {
			break
		}
		b.WriteString(cluster)
		width -= clusterWidth
		s = s[len(cluster):]
	}
	return b.String()
}

func clusterCells(cluster string) int {
	if strings.ContainsRune(cluster, zwj) {
		width := 0
		for part := range strings.SplitSeq(cluster, string(zwj)) {
			if widths.zwjJoined {
				width = max(width, clusterCells(part))
			} else {
				width += clusterCells(part)
			}
		}
		return width
	}
	if isFlagPair(cluster) {
		return widths.flagPair
	}
	if strings.ContainsFunc(cluster, isSkinToneModifier) {
		return widths.skinTone
	}
	if strings.ContainsRune(cluster, vs16) {
		base := strings.ReplaceAll(cluster, string(vs16), "")
		if ansi.StringWidth(base) >= 2 {
			return 2
		}
		return widths.vs16
	}
	if marks := countSpacingMarks(cluster); marks > 0 {
		base := strings.Map(dropSpacingMark, cluster)
		return ansi.StringWidth(base) + marks*widths.spacingMark
	}
	return ansi.StringWidth(cluster)
}

func isFlagPair(cluster string) bool {
	runes := []rune(cluster)
	return len(runes) == 2 && isRegionalIndicator(runes[0]) && isRegionalIndicator(runes[1])
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1f1e6 && r <= 0x1f1ff
}

func isSkinToneModifier(r rune) bool {
	return r >= 0x1f3fb && r <= 0x1f3ff
}

func countSpacingMarks(cluster string) int {
	marks := 0
	for _, r := range cluster {
		if unicode.Is(unicode.Mc, r) {
			marks++
		}
	}
	return marks
}

func dropSpacingMark(r rune) rune {
	if unicode.Is(unicode.Mc, r) {
		return -1
	}
	return r
}
