package workspace

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// The rows above a column's cards: the colored bar across the top, then the
// column's name and the blank line under it. All three stay put while the cards
// scroll beneath them.
const (
	columnBandRows    = 1
	columnHeadingRows = 2
)

// The bar across the top of a column. The cursor's column gets a whole cell and
// every other one the upper half of it, so where the reader is standing reads as
// a thicker bar without anything having to move.
const (
	bandGlyph        = "▀"
	bandGlyphFocused = "█"
)

// How much of its color a column mixes into the background behind its cards.
// The web washes the same color over the canvas with color-mix; these are the
// strengths that survive being drawn in a terminal — enough to tell four columns
// apart at a glance, not enough to fight the text.
//
// An uncolored column takes far more of the theme's own border color, because
// that color is already close to the background. Without a wash the columns have
// no edges, and a board with no edges is a wall of cards.
const (
	coloredWash   = 0.16
	uncoloredWash = 0.45

	// How much of its color a colored column lends the border of the cards in
	// it, which is what the web does with --card-border-color.
	cardEdgeTint = 0.55
)

// The marks a wormhole is labeled with: one that leads somewhere, and one whose
// far end has been archived or trashed.
//
// A spiral, and no fallback for it. There is no way to ask a terminal whether it
// can draw an emoji — CalibrateWidths asks how wide one is, which is a different
// question — so this follows the folders' 📁 and the sidebar's ✨ and trusts the
// terminal. What is asked is the width, which is what a strip has to lay out
// around.
const (
	wormholeGlyph       = "🌀"
	brokenWormholeGlyph = "◌"
)

// The shadow a carried card casts. One cell down and one to the right, drawn as
// solid cells of a darkened wash: a terminal has no blur, and a hard offset edge
// is what reads as height at this size.
const (
	// Down the right edge, a left half-block hugs the card; along the bottom, a
	// lower half-block hugs it from below. Half-blocks rather than whole ones so
	// the shadow sits tight against the card instead of a cell away from it.
	shadowEdgeGlyph  = "▌"
	shadowUnderGlyph = "▄"
	shadowCells      = 1

	// How far toward black the wash goes to make the shadow. Enough to see
	// against a colored column, not so much that it reads as a hole.
	shadowDepth = 0.5

	// What a folded column shows instead, having no room to hover a card over.
	dropHereGlyph = "▾"
)

// How many lines of a card's title the board shows before it cuts the rest. The
// web stops at three too.
const cardTitleLines = 3

// The room a card's border and the space inside it take out of a column: a cell
// of wash down either side, the two border glyphs, and a space inside each of
// those.
const cardChrome = 6

// columnPaint is the palette one column is drawn in. Every column mixes its own,
// which is the whole point of coloring them.
//
// Each style carries the ground it is drawn on as well as its own color, and
// nothing here inherits a background. It cannot: a nested style ends with a
// reset, and the reset drops the background the outer style had laid down, so a
// row is built by putting finished spans next to each other rather than by
// wrapping one in another.
type columnPaint struct {
	// On the column's own wash: the bar, blank space, the heading and its count,
	// and the border around a card.
	band  lipgloss.Style
	wash  lipgloss.Style
	title lipgloss.Style
	count lipgloss.Style
	edge  lipgloss.Style

	// here is the border of the card under the cursor. The rest of the column's
	// cards keep edge: a whole column of lit borders says which column the
	// reader is in, which the bar above it already said, and loses which card
	// they are standing on.
	here lipgloss.Style

	// shadow is what a carried card casts on the column under it, and vortex the
	// spiral turning under a wormhole's destination.
	shadow lipgloss.Style
	vortex lipgloss.Style

	// On a card's own ground: its padding, its title, and the line under it.
	card lipgloss.Style
	head lipgloss.Style
	foot lipgloss.Style
}

// paintFor mixes the palette for one column.
//
// A theme with no colors at all gets no backgrounds either: a color mixed toward
// lipgloss.NoColor comes back black, and a board painted black is worse than a
// board painted nothing.
func (t *cardTableScreen) paintFor(column *cardColumn, selected bool) columnPaint {
	theme := t.ctx.Styles().Theme()

	named := theme.Foreground
	if selected {
		named = theme.Primary
	}

	paint := columnPaint{
		band:   lipgloss.NewStyle(),
		wash:   lipgloss.NewStyle(),
		title:  lipgloss.NewStyle().Bold(true).Foreground(named),
		count:  lipgloss.NewStyle().Foreground(theme.Muted),
		edge:   lipgloss.NewStyle().Foreground(theme.Border),
		here:   lipgloss.NewStyle().Foreground(theme.Primary),
		shadow: lipgloss.NewStyle().Foreground(theme.Muted),
		vortex: lipgloss.NewStyle().Foreground(theme.Muted),
		card:   lipgloss.NewStyle(),
		head:   lipgloss.NewStyle().Foreground(theme.Foreground),
		foot:   lipgloss.NewStyle().Foreground(theme.Muted),
	}
	if theme.Colorless() {
		return paint
	}

	tone, painted := theme.CardColor(column.paint())
	strength := coloredWash
	if !painted {
		tone, strength = theme.Border, uncoloredWash
	}

	// The cards in an uncolored column keep the theme's border. In a colored one
	// they take a muted share of the color, which is what ties a card to the
	// column it is sitting in.
	rim := theme.Border
	if painted {
		rim = tui.Tint(theme.Background, tone, cardEdgeTint)
	}

	washed := tui.Tint(theme.Background, tone, strength)
	paint.band = paint.band.Foreground(tone).Background(washed)
	paint.wash = paint.wash.Background(washed)
	paint.title = paint.title.Background(washed)
	paint.count = paint.count.Background(washed)
	paint.edge = paint.edge.Foreground(rim).Background(washed)
	paint.here = paint.here.Background(washed)
	paint.shadow = paint.shadow.
		Foreground(tui.Tint(washed, lipgloss.Black, shadowDepth)).
		Background(washed)

	// The spiral is drawn in the column's own color rather than in the theme's
	// muted grey: a wormhole colored aqua should turn aqua.
	paint.vortex = paint.vortex.Foreground(tone).Background(washed)

	paint.card = paint.card.Background(theme.Background)
	paint.head = paint.head.Background(theme.Background)
	paint.foot = paint.foot.Background(theme.Background)
	return paint
}

// row lays out one row of a column: the pieces go down in order and the wash
// fills whatever is left over, so the column comes back exactly as wide as it
// was asked for. A row that stops short lets the column beside it show through.
func (p columnPaint) row(width int, pieces ...string) string {
	drawn := strings.Join(pieces, "")
	return drawn + p.blank(width-tui.DisplayWidth(drawn))
}

// blank is a run of the column's own wash.
func (p columnPaint) blank(cells int) string {
	if cells <= 0 {
		return ""
	}
	return p.wash.Render(strings.Repeat(" ", cells))
}

// centered puts one piece in the middle of the row, which is how a folded
// column writes everything it has room for.
func (p columnPaint) centered(width int, piece string) string {
	return p.row(width, p.blank((width-tui.DisplayWidth(piece))/2), piece)
}

// --- A column ---

// renderColumn draws one column, exactly as many rows tall as the board is.
func (t *cardTableScreen) renderColumn(column *cardColumn, width int, selected bool) string {
	paint := t.paintFor(column, selected)
	switch {
	case column.kind == columnWormhole:
		return t.renderWormhole(column, paint, width, selected)
	case column.collapsed:
		return t.renderStrip(column, paint, width, selected)
	}

	rows := make([]string, 0, t.height)
	rows = append(rows, renderBand(paint, width, selected))
	rows = append(rows, renderHeading(column, paint, width))
	rows = append(rows, paint.row(width))

	// A card being carried hovers over the column it would land in, above
	// whatever is already there — which is where it would go if it were put down.
	if t.hovering(column) {
		rows = append(rows, t.renderHeld(paint, width)...)
	}
	rows = append(rows, t.renderCards(column, paint, width, selected)...)
	return strings.Join(t.fill(rows, paint, width), "\n")
}

// hovering is whether the card in the air is over this column.
func (t *cardTableScreen) hovering(column *cardColumn) bool {
	return t.held != nil && t.held.at < len(t.columns) && t.columns[t.held.at] == column
}

// renderHeld draws the card in the air: lifted a row off the column and casting
// a shadow down and to the right of itself, the way the web lifts one under a
// pointer.
//
// The shadow is what says the card is above the column rather than in it. It is
// the column's own wash darkened — there is no black in a terminal's palette to
// reach for, only a color mixed further toward one.
func (t *cardTableScreen) renderHeld(paint columnPaint, width int) []string {
	// The card is drawn to the full width and then cut back by the cell the
	// shadow takes, so the two are touching. A card drawn a cell narrower would
	// leave its own trailing wash between itself and its shadow, and a shadow
	// with a gap under it is two objects rather than one lifted.
	drawn := renderCard(t.held.card, paint, width, t.now(), true)

	rows := make([]string, 0, len(drawn)+2)
	for _, line := range drawn {
		rows = append(rows, paint.row(width,
			ansi.Truncate(line, max(width-shadowCells, 0), ""),
			paint.shadow.Render(shadowEdgeGlyph)))
	}

	// The shadow's own row, starting one cell in from the card's left edge so
	// the two runs meet at the corner rather than crossing.
	rows = append(rows,
		paint.row(width, paint.blank(2), paint.shadow.Render(strings.Repeat(shadowUnderGlyph, max(width-2, 0)))),
		paint.row(width))
	return rows
}

func renderBand(paint columnPaint, width int, selected bool) string {
	glyph := bandGlyph
	if selected {
		glyph = bandGlyphFocused
	}
	return paint.band.Render(strings.Repeat(glyph, max(width, 0)))
}

// renderHeading is the column's name with what it holds after it, the way the
// web writes "In progress (2)". The count sits against the right edge and gives
// up its place to the name when the column is too narrow for both.
func renderHeading(column *cardColumn, paint columnPaint, width int) string {
	inner := max(width-2, 1)
	name := truncateToWidth(column.title, inner)
	held := column.held()

	room := inner - tui.DisplayWidth(name) - tui.DisplayWidth(held) - 1
	if held == "" || room < 0 {
		return paint.row(width, paint.blank(1), paint.title.Render(name))
	}
	return paint.row(width,
		paint.blank(1),
		paint.title.Render(name),
		paint.blank(room+1),
		paint.count.Render(held))
}

// held is how many cards the column has, and how many of those are parked, with
// the watching mark in front when the reader is following it.
//
// The count is the API's rather than the length of what was read: a deep column
// is read a page at a time, and the heading should still say how deep it is.
func (c *cardColumn) held() string {
	if c.kind == columnWormhole {
		return ""
	}

	count := "(" + strconv.Itoa(c.count) + ")"
	if c.onHold > 0 {
		count = "(" + strconv.Itoa(c.count) + " · " + strconv.Itoa(c.onHold) + " on hold)"
	}
	if c.watching {
		return watchedGlyph + " " + count
	}
	return count
}

// fill pads a column out to the board's height with its own wash, and cuts it
// back when it has overrun. Every column has to come back the same height, or
// the row they are joined along stops being a row.
func (t *cardTableScreen) fill(rows []string, paint columnPaint, width int) []string {
	blank := paint.row(width)
	for len(rows) < t.height {
		rows = append(rows, blank)
	}
	return rows[:max(t.height, 0)]
}

// --- The cards in a column ---

// cardRow is one drawn row of a column's cards, and which card it belongs to. A
// card is several rows and the cursor stands on all of them, which is what keeps
// scrolling from stopping halfway down one.
type cardRow struct {
	text string
	card int
}

// cardsHeight is the rows left for cards once the bar and the heading have taken
// theirs.
func (t *cardTableScreen) cardsHeight() int {
	return max(t.height-columnBandRows-columnHeadingRows, 0)
}

// renderCards is the column's cards, scrolled to wherever its own cursor has
// walked, with a note along the bottom when there are more below.
func (t *cardTableScreen) renderCards(column *cardColumn, paint columnPaint, width int, selected bool) []string {
	height := t.cardsHeight()
	rows := t.cardRows(column, paint, width, selected)
	if height <= 0 || len(rows) == 0 {
		return nil
	}

	from := min(column.offset, max(len(rows)-1, 0))
	end := wholeCards(rows, from, min(from+t.cardRoom(rows), len(rows)))

	drawn := make([]string, 0, height)
	for _, row := range rows[from:end] {
		drawn = append(drawn, row.text)
	}

	// The board is only ever as tall as the terminal, so a column deeper than
	// that says how much is under the fold. The note goes on the bottom row
	// rather than wherever the cards happened to stop: a count halfway up the
	// column reads as one more card.
	below := column.below(rows, end)
	if below <= 0 {
		return drawn
	}
	for len(drawn) < height-1 {
		drawn = append(drawn, paint.row(width))
	}
	note := truncateToWidth("↓ "+strconv.Itoa(below)+" more", max(width-2, 1))
	return append(drawn, paint.row(width, paint.blank(1), paint.count.Render(note)))
}

// cardRoom is how many rows the cards themselves may take: all of them, unless
// there is more below than fits and the bottom row has to say so.
func (t *cardTableScreen) cardRoom(rows []cardRow) int {
	if len(rows) > t.cardsHeight() {
		return max(t.cardsHeight()-1, 1)
	}
	return t.cardsHeight()
}

// wholeCards backs a window off any card it would cut through, so a column ends
// on a card's bottom border rather than halfway down its title.
func wholeCards(rows []cardRow, from, end int) int {
	for end > from+1 && end < len(rows) && rows[end-1].card == rows[end].card {
		end--
	}
	return end
}

// below is how many cards sit past the last row on screen — the ones scrolled
// off the bottom, plus the ones on the pages nobody asked for.
//
// The window's own last row is often a gap between two cards, so this walks back
// to the last row that belongs to one.
func (c *cardColumn) below(rows []cardRow, end int) int {
	unread := max(c.count-len(c.cards), 0)
	if end <= 0 || end >= len(rows) {
		return unread
	}
	for at := end - 1; at >= 0; at-- {
		if shown := rows[at].card; shown >= 0 {
			return len(c.cards) - shown - 1 + unread
		}
	}
	return len(c.cards) + unread
}

// cardRows draws every card in the column, in order, with a blank row between
// them. Both the drawing and the scrolling read this, so what the cursor stands
// on and what is on screen are worked out from the same rows.
func (t *cardTableScreen) cardRows(column *cardColumn, paint columnPaint, width int, selected bool) []cardRow {
	switch {
	case column.reading && !column.loaded:
		return []cardRow{{
			text: paint.row(width, paint.blank(1), paint.count.Render("Loading…")),
			card: noItem,
		}}
	case len(column.cards) == 0:
		return dropZone(paint, width)
	}

	now := t.now()
	rows := make([]cardRow, 0, len(column.cards)*5)
	for index, on := range column.cards {
		here := selected && index == column.cursor
		for _, line := range renderCard(on, paint, width, now, here) {
			rows = append(rows, cardRow{text: line, card: index})
		}
		rows = append(rows, cardRow{text: paint.row(width), card: noItem})
	}
	return rows
}

// dropZone is what an empty column shows: the dashed box the web draws where a
// card would land. A column with nothing in it and nothing drawn in it reads as
// the end of the board.
func dropZone(paint columnPaint, width int) []cardRow {
	across := max(width-cardChrome+2, 1)
	dashes := strings.Repeat("╌", across)
	side := paint.edge.Render("╎")

	return []cardRow{
		{text: paint.row(width, paint.blank(1), paint.edge.Render("╭"+dashes+"╮")), card: noItem},
		{text: paint.row(width, paint.blank(1), side, paint.blank(across), side), card: noItem},
		{text: paint.row(width, paint.blank(1), paint.edge.Render("╰"+dashes+"╯")), card: noItem},
	}
}

// renderCard draws one card: its title over as many lines as it takes, then who
// is holding it and how far along it is.
//
// The web draws a card as a panel sitting on the column's wash, and so does
// this: the border takes a muted share of the column's color, the inside goes
// back to the plain background, and the wash showing on all four sides is what
// makes it read as a card rather than a paragraph.
func renderCard(on card, paint columnPaint, width int, now time.Time, selected bool) []string {
	inner := max(width-cardChrome, 1)
	rule := strings.Repeat("─", inner+2)

	edge := paint.edge
	if selected {
		edge = paint.here
	}

	rows := make([]string, 0, cardTitleLines+3)
	rows = append(rows, paint.row(width, paint.blank(1), edge.Render("╭"+rule+"╮")))
	for _, line := range cardFace(on, paint, inner, now, selected) {
		rows = append(rows, paint.row(width,
			paint.blank(1),
			edge.Render("│"),
			paint.card.Render(" "),
			line,
			paint.card.Render(strings.Repeat(" ", max(inner-tui.DisplayWidth(line), 0))+" "),
			edge.Render("│")))
	}
	rows = append(rows, paint.row(width, paint.blank(1), edge.Render("╰"+rule+"╯")))
	return rows
}

// cardFace is what is written inside a card: its title over up to three lines,
// then the quiet line under it.
func cardFace(on card, paint columnPaint, width int, now time.Time, selected bool) []string {
	head := paint.head
	if selected {
		head = head.Bold(true)
	}

	wrapped := wrapText(on.title, width)
	if len(wrapped) > cardTitleLines {
		wrapped = wrapped[:cardTitleLines]
		wrapped[cardTitleLines-1] = truncateToWidth(wrapped[cardTitleLines-1]+" …", width)
	}

	lines := make([]string, 0, len(wrapped)+1)
	for _, line := range wrapped {
		lines = append(lines, head.Render(truncateToWidth(line, width)))
	}
	return append(lines, paint.foot.Render(on.byline(now, width)))
}

// byline is the quiet line under a card's title: who has it, when it turned up,
// and how far along it is.
//
// It sheds rather than cuts. The badges are what the eye came for — how much of
// the card is done, how much has been said about it — so the date goes first and
// the name is shortened around them.
func (c card) byline(now time.Time, width int) string {
	who, badges := c.who(), c.progress()

	for _, attempt := range [][]string{{who, c.on(now), badges}, {who, badges}} {
		if line := strings.Join(nonEmpty(attempt...), " · "); tui.DisplayWidth(line) <= width {
			return line
		}
	}

	if room := width - tui.DisplayWidth(badges) - 3; badges != "" && room >= minTrailingRoom {
		return truncateToWidth(who, room) + " · " + badges
	}
	// Down to a choice between the two, the badges win: half a name says less
	// than a whole count.
	if tui.DisplayWidth(badges) <= width {
		return badges
	}
	return truncateToWidth(strings.Join(nonEmpty(who, badges), " · "), width)
}

// who a card belongs to. Assignees come first when it has any: the web shows
// both — the author in the byline, the assignee as an avatar in the corner — and
// on a board the question is who is holding a card rather than who filed it.
func (c card) who() string {
	if len(c.assignees) > 0 {
		return strings.Join(shortNames(c.assignees), ", ")
	}
	return shortName(c.author)
}

// on is the day a card turned up, worded the way the web words it — and how long
// ago instead once it is recent enough for that to be the more useful answer.
func (c card) on(now time.Time) string {
	switch {
	case c.at.IsZero():
		return ""
	case now.Sub(c.at) < 24*time.Hour:
		return since(c.at, now)
	default:
		return c.at.Format("Jan 2")
	}
}

// progress is how much of the card is done and how much has been said about it:
// its steps, then its comments. Both are dropped when there are none — an empty
// checklist and an unanswered card have nothing to say.
func (c card) progress() string {
	var badges []string
	if c.steps > 0 {
		badges = append(badges, "✓"+strconv.Itoa(c.stepsDone)+"/"+strconv.Itoa(c.steps))
	}
	if c.comments > 0 {
		badges = append(badges, "●"+strconv.Itoa(c.comments))
	}
	return strings.Join(badges, " ")
}

// shortName is a person as a card names them: a first name and the initial of
// whatever follows it, which is how the web writes "Michael B.".
func shortName(who string) string {
	first, rest, found := strings.Cut(strings.TrimSpace(who), " ")
	if !found || rest == "" {
		return who
	}
	return first + " " + string([]rune(rest)[0]) + "."
}

func shortNames(people []string) []string {
	short := make([]string, 0, len(people))
	for _, who := range people {
		short = append(short, shortName(who))
	}
	return short
}

// --- A folded column ---

// renderStrip draws a folded column: the bar across the top, what it holds, and
// its name written one letter to a row.
//
// The web turns the name on its side. A terminal cannot rotate a glyph, so the
// letters stack instead — which reads the same way and takes the same strip.
func (t *cardTableScreen) renderStrip(column *cardColumn, paint columnPaint, width int, selected bool) string {
	rows := make([]string, 0, t.height)
	rows = append(rows, renderBand(paint, width, selected))
	rows = append(rows, stripLabel(column, paint, width))

	// A strip has no room to hover a card over, so an arrow stands for one: the
	// reader is carrying something and this is where it would go.
	if t.hovering(column) {
		rows = append(rows, paint.centered(width, paint.title.Render(dropHereGlyph)))
	}
	rows = append(rows, paint.row(width))

	letters := paint.title
	if !selected {
		letters = paint.count
	}
	for _, letter := range strings.ToUpper(column.title) {
		rows = append(rows, paint.centered(width, letters.Render(string(letter))))
	}
	return strings.Join(t.fill(rows, paint, width), "\n")
}

// stripLabel is the row under a folded column's bar: how many cards it holds.
func stripLabel(column *cardColumn, paint columnPaint, width int) string {
	return paint.centered(width, paint.count.Render(truncateToWidth(column.held(), width)))
}

// --- A wormhole ---

// renderWormhole draws a way off the board: the spiral, then where it comes out,
// written the way the web writes it — the project, the table, the column.
//
// Neither a column nor a strip. A wormhole holds nothing, so a full column of
// cards would be a column of nothing; and its name is a path three segments long,
// which no strip has room for. What it needs is to say where it goes.
func (t *cardTableScreen) renderWormhole(column *cardColumn, paint columnPaint, width int, selected bool) string {
	glyph, mark := wormholeGlyph, paint.title
	if !column.linked {
		glyph, mark = brokenWormholeGlyph, paint.count
	}

	rows := make([]string, 0, t.height)
	rows = append(rows, renderBand(paint, width, selected))
	rows = append(rows, paint.row(width, paint.blank(1), mark.Render(glyph)))
	rows = append(rows, paint.row(width))

	// A card being carried over a wormhole is about to leave the board, so the
	// arrow says so where the destination would be read.
	if t.hovering(column) {
		rows = append(rows, paint.centered(width, paint.title.Render(dropHereGlyph)), paint.row(width))
	}

	if !column.linked {
		rows = append(rows, paint.row(width, paint.blank(1),
			paint.count.Render(truncateToWidth("Goes nowhere", max(width-2, 1)))))
		return strings.Join(t.fill(rows, paint, width), "\n")
	}

	// The whole path, a segment to a line, with the column it lands in last and
	// named plainly — that is the part a reader is aiming at.
	segments := strings.Split(column.destination, breadcrumbSeparator)
	for index, segment := range segments {
		style := paint.count
		if index == len(segments)-1 {
			style = paint.title
		}
		for _, line := range wrapText(strings.TrimSpace(segment), max(width-2, 1)) {
			rows = append(rows, paint.row(width, paint.blank(1), style.Render(line)))
		}
	}

	// Whatever is left under the destination turns, so a wormhole looks like one
	// rather than like a column somebody forgot to fill.
	rows = append(rows, paint.row(width))
	rows = append(rows, t.vortexRows(paint, width, t.height-len(rows))...)
	return strings.Join(t.fill(rows, paint, width), "\n")
}
