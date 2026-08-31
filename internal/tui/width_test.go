package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withWidths runs fn against a given calibration and puts the defaults back.
func withWidths(t *testing.T, table clusterWidths, fn func()) {
	t.Helper()

	was := widths
	t.Cleanup(func() { widths = was })
	widths = table
	fn()
}

func TestDisplayWidthPlainText(t *testing.T) {
	assert.Equal(t, 0, DisplayWidth(""))
	assert.Equal(t, 5, DisplayWidth("hello"))

	// Styling is not counted, the same as lipgloss.Width.
	styled := lipgloss.NewStyle().Bold(true).Render("hello")
	assert.Equal(t, 5, DisplayWidth(styled))
}

// A multi-line string measures its widest line, so this drops in wherever
// lipgloss.Width was measuring a rendered block.
func TestDisplayWidthMeasuresTheWidestLine(t *testing.T) {
	assert.Equal(t, 5, DisplayWidth("hi\nhello\nyo"))
	assert.Equal(t, lipgloss.Width("hi\nhello\nyo"), DisplayWidth("hi\nhello\nyo"))
}

// Wide characters take two cells, which the width tables do agree on.
func TestDisplayWidthWideCharacters(t *testing.T) {
	assert.Equal(t, 4, DisplayWidth("日本"))
	assert.Equal(t, 6, DisplayWidth("한국어"))
}

// A spacing combining mark is the case the tables leave open: a Devanagari
// matra or a Thai vowel sign either adds a cell or rides along in its base's.
func TestDisplayWidthSpacingMarks(t *testing.T) {
	// की is क plus the matra ी — two cells where the mark takes one, one where
	// the terminal draws it over the base.
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 2, DisplayWidth("की"))
	})
	withWidths(t, clusterWidths{spacingMark: 0, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 1, DisplayWidth("की"))
	})
}

// Thai stacks its marks the same way, and Hindi words are several of them in a
// row — a cell out on each one shears the whole row.
func TestDisplayWidthHindiAndThai(t *testing.T) {
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 4, DisplayWidth("कीकी"))
		assert.Positive(t, DisplayWidth("ก่อน"))
	})
}

// Arabic is joined rather than combined, so its letters are one cell each and
// need no calibration.
func TestDisplayWidthArabic(t *testing.T) {
	assert.Equal(t, 5, DisplayWidth("مرحبا"))
}

func TestDisplayWidthFlagPair(t *testing.T) {
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 2, DisplayWidth("🇭🇷"))
	})
	// A terminal that draws each regional indicator on its own reports four.
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 4, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 4, DisplayWidth("🇭🇷"))
	})
}

func TestDisplayWidthSkinTone(t *testing.T) {
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 2, DisplayWidth("👍🏽"))
	})
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 4, zwjJoined: true}, func() {
		assert.Equal(t, 4, DisplayWidth("👍🏽"))
	})
}

// A ZWJ sequence is one glyph on a terminal that joins and several on one that
// does not, which is a three-cell difference on a single family emoji.
func TestDisplayWidthZWJSequence(t *testing.T) {
	family := "👨‍👩‍👧"

	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 2, DisplayWidth(family))
	})
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: false}, func() {
		assert.Equal(t, 6, DisplayWidth(family))
	})
}

// A variation selector turns a text-default symbol into an emoji, which some
// terminals then draw at two cells and some at one.
func TestDisplayWidthVariationSelector(t *testing.T) {
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 2, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 2, DisplayWidth("✈️"))
	})
	withWidths(t, clusterWidths{spacingMark: 1, vs16: 1, flagPair: 2, skinTone: 2, zwjJoined: true}, func() {
		assert.Equal(t, 1, DisplayWidth("✈️"))
	})
}

// --- Cutting between glyphs ---

func TestFirstCluster(t *testing.T) {
	cluster, width := FirstCluster("की rest")
	assert.Equal(t, "की", cluster)
	assert.Equal(t, DisplayWidth("की"), width)

	cluster, width = FirstCluster("")
	assert.Equal(t, "", cluster)
	assert.Equal(t, 0, width)
}

// A cut lands between glyphs, never inside one: half a combining sequence is a
// fragment the terminal draws at a width nobody predicted.
func TestFitGraphemesKeepsClustersWhole(t *testing.T) {
	assert.Equal(t, "abc", FitGraphemes("abcdef", 3))
	assert.Equal(t, "", FitGraphemes("की", 1))
	assert.Equal(t, "की", FitGraphemes("कीकी", 2))

	// Never a base without its mark, nor a mark without its base.
	for width := range 8 {
		kept := FitGraphemes("कीकीकी", width)
		assert.Equal(t, 0, countSpacingMarks(kept)-strings.Count(kept, "क"),
			"width %d cut a cluster in half: %q", width, kept)
	}
}

func TestFitGraphemesNeverExceedsItsWidth(t *testing.T) {
	for _, s := range []string{"hello", "日本語", "कीकी", "👨‍👩‍👧 family", "مرحبا"} {
		for width := range 10 {
			kept := FitGraphemes(s, width)
			assert.LessOrEqual(t, DisplayWidth(kept), width, "%q at width %d", s, width)
		}
	}
}

// --- Reading the terminal's answers ---

func TestParseCursorReports(t *testing.T) {
	assert.Equal(t, []int{3, 2}, parseCursorReports([]byte("\x1b[1;3R\x1b[1;2R")))

	// A keystroke landing mid-probe must not derail the answers behind it.
	assert.Equal(t, []int{3, 2}, parseCursorReports([]byte("\x1b[1;3Rq\x1b[1;2R")))
	assert.Empty(t, parseCursorReports([]byte("no reports here")))
}

func TestDeriveWidths(t *testing.T) {
	// Columns are one past the width drawn, so a two-cell cluster reports 3.
	table, ok := deriveWidths([]int{3, 3, 3, 3, 3})
	require.True(t, ok)
	assert.Equal(t, 1, table.spacingMark)
	assert.Equal(t, 2, table.vs16)
	assert.Equal(t, 2, table.flagPair)
	assert.Equal(t, 2, table.skinTone)
	assert.True(t, table.zwjJoined)

	// A terminal that draws the family sequence unjoined reports it wide.
	table, ok = deriveWidths([]int{3, 3, 3, 3, 7})
	require.True(t, ok)
	assert.False(t, table.zwjJoined)
}

// A report outside the plausible range means the probe was disturbed — a resize,
// a wrapped line — and the whole calibration is discarded rather than half
// trusted.
func TestDeriveWidthsRejectsDisturbedProbes(t *testing.T) {
	_, ok := deriveWidths([]int{3, 3, 3})
	assert.False(t, ok, "a short answer was accepted")

	_, ok = deriveWidths([]int{1, 3, 3, 3, 3})
	assert.False(t, ok, "a zero-width cluster was accepted")

	_, ok = deriveWidths([]int{3, 3, 3, 3, 99})
	assert.False(t, ok, "an implausible width was accepted")
}

// Every probe has to come back for the table to be built, so the request asks
// for exactly as many reports as it will read.
func TestProbeRequestAsksOncePerCluster(t *testing.T) {
	request := probeRequest()

	assert.Equal(t, len(widthProbes), strings.Count(request, cursorReport))
	for _, probe := range widthProbes {
		assert.Contains(t, request, probe)
	}
	assert.True(t, strings.HasPrefix(request, probeSetup))
	assert.True(t, strings.HasSuffix(request, probeCleanup))
}

// The defaults err wide: over-allocating shows as a one-cell gap beside a glyph,
// under-allocating shears the row it sits on.
func TestDefaultWidthsErrWide(t *testing.T) {
	assert.Equal(t, 1, widths.spacingMark)
	assert.Equal(t, 2, widths.vs16)
	assert.Equal(t, 2, widths.flagPair)
	assert.Equal(t, 2, widths.skinTone)
	assert.True(t, widths.zwjJoined)
}
