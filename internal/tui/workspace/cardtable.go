package workspace

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// cardTableKind is the dock's own name for a card table, which is how the tool
// arrives from the API.
const cardTableKind = "kanban_board"

// How many cards a column asks for. A column deep enough to need a second page
// is a column nobody reads to the bottom of, so the board says how many are
// below rather than walking there.
const cardPageSize = 50

// The keys the board answers, beyond the arrows.
const (
	addCardKey        = "a"
	addColumnKey      = "c"
	editColumnKey     = "e"
	watchColumnKey    = "w"
	collapseColumnKey = "z"
	refreshBoardKey   = "r"

	// moveCardKey picks a card up, and puts it down again. Carrying it is what
	// the arrows do while it is in the air.
	moveCardKey = "m"
)

// columnKind is which of a card table's lists this is. Basecamp puts three on
// every board it makes — Triage, Not now, Done — and they behave differently
// enough from the columns a reader adds that the board has to know which is
// which: the two at the far end arrive collapsed, the way the web leaves them.
type columnKind int

const (
	columnRegular columnKind = iota
	columnTriage
	columnNotNow
	columnDone
	columnWormhole
)

// columnKinds maps the recordable Basecamp records a list as to what the board
// makes of it. See Kanban::Board::DefaultColumns.
var columnKinds = map[string]columnKind{
	"Kanban::Triage":        columnTriage,
	"Kanban::NotNowColumn":  columnNotNow,
	"Kanban::DoneColumn":    columnDone,
	"Kanban::Column":        columnRegular,
	"Kanban::Wormhole":      columnWormhole,
	"Kanban::OnHoldColumn":  columnRegular,
	"Kanban::CardColumnRow": columnRegular,
}

// cardColumn is one list on the table: the cards in it, where the reader is
// standing in it, and how much of it is on screen.
//
// Each column scrolls on its own, the way each column on the web has its own
// scrollbar. Walking down a deep column and stepping across to a shallow one
// leaves the deep one where it was.
type cardColumn struct {
	id    int64
	title string
	color string
	kind  columnKind

	// count is what the API says the column holds. It is not always what is
	// loaded: a collapsed column has read nothing at all, and a deep one is read
	// a page at a time.
	count int

	// onHold is a column's parked cards, which Basecamp keeps in a list of their
	// own hanging off it. The board says how many there are without opening it.
	onHold int

	cards  []card
	cursor int
	offset int

	collapsed bool
	loaded    bool
	reading   bool

	// watching is whether the reader is subscribed to the column, and
	// knowsWatching whether that has been asked yet. A column's payload says who
	// is subscribed and never whether that includes the reader, so the answer
	// arrives with the first w rather than with the board.
	watching      bool
	knowsWatching bool

	// where a wormhole leads, and whether it still leads anywhere. Both are
	// empty for every other kind of column.
	destination string
	linked      bool

	// Where a wormhole comes out, as the column and the project it is in, which
	// is what going through one needs. The bucket is another project's: crossing
	// projects is the whole point of a wormhole.
	destinationColumn int64
	destinationBucket int64
}

// card is one card, flattened to what the board draws and what the screen
// behind it shows.
//
// The description comes along ready to draw, pictures and all: the API renders
// it into the column's own index, so the screen behind a card opens with the
// words already in hand.
type card struct {
	id    int64
	title string

	// words is the description ready to draw, read once here so the screen
	// behind the card needs no request of its own.
	words body

	author    string
	avatar    string
	assignees []string

	steps     int
	stepsDone int
	stepList  []cardStep
	comments  int

	at time.Time
}

// cardStep is one line of the checklist a card was broken into.
type cardStep struct {
	title string
	who   string
	done  bool
}

// cardTableLoadedMsg is the table and its lists. cardsLoadedMsg is one column's
// cards, which are a read of their own — the table carries counts and colors,
// never the cards.
type cardTableLoadedMsg struct {
	columns []*cardColumn
	err     error
}

type cardsLoadedMsg struct {
	column int64
	cards  []card
	err    error
}

// cardMoveFailedMsg is a move the server refused after the board had already
// drawn it. The board puts the card back by reading both columns again.
type cardMoveFailedMsg struct {
	from int64
	to   int64
	err  error
}

// openCardMsg asks the model for the screen behind one card.
type openCardMsg struct{ card card }

// cardTableScreen is a project's card table: its columns side by side, the cards
// in them, and a cursor that walks both.
//
// The web scrolls the board sideways under a fixed header, with Triage laid
// across the top and Not now and Done pinned collapsed to the right edge. This
// keeps the columns in that order and scrolls the same way. What a terminal
// cannot do is rotate a glyph, so a collapsed column writes its name one letter
// to a row instead of on its side.
type cardTableScreen struct {
	ctx    *Context
	board  tool
	inside project

	columns []*cardColumn

	// pending counts the reads still in flight, which is what the dashes beside
	// the heading say.
	pending int
	notice  string

	// cursor is the column the reader is standing in; first is the leftmost
	// column drawn, which is how the board scrolls sideways.
	cursor int
	first  int

	// held is the card in the air, and nil when the reader is not carrying one.
	held *carried

	// turn is which frame the wormholes' spirals are on, and turning whether a
	// turn is already in flight.
	turn    int
	turning bool

	width  int
	height int

	now func() time.Time
}

func newCardTable(ctx *Context, board tool, inside project) *cardTableScreen {
	return &cardTableScreen{ctx: ctx, board: board, inside: inside, now: time.Now}
}

// Init reads the table. It does not clear the board first: this is what r runs
// too, and a refresh that forgot which columns the reader had folded would put
// them back to arranging it.
func (t *cardTableScreen) Init() tea.Cmd {
	t.pending++
	t.notice = ""
	return loadCardTable(t.ctx.Ctx(), t.ctx.app, t.board.id)
}

func (t *cardTableScreen) Title() string { return t.board.name }

func (t *cardTableScreen) Loading() bool { return false }

func (t *cardTableScreen) Resize(width, height int) {
	t.width = width
	t.height = height
	t.scrollToCursor()
}

func (t *cardTableScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case cardTableLoadedMsg:
		t.pending = max(t.pending-1, 0)
		if msg.err != nil {
			t.notice = errorNotice("Could not load the card table", msg.err)
			return nil, true
		}
		t.columns = adopt(msg.columns, t.columns)
		t.clampCursor()
		// The wormholes are only known now, and they are what the spiral turns
		// for, so this is the first moment there is anything to arm.
		return tea.Batch(t.readOpenColumns(), t.turnVortex()), true

	case cardsLoadedMsg:
		t.pending = max(t.pending-1, 0)
		column := t.columnByID(msg.column)
		if column == nil {
			return nil, true
		}
		column.reading = false
		if msg.err != nil {
			// The rest of the board is still good, so a column that would not
			// read says so in its own strip rather than replacing the board with
			// a notice. Pressing r asks again.
			return notifyError("Could not load "+column.title, msg.err), true
		}
		column.loaded = true
		column.cards = msg.cards
		column.cursor = min(column.cursor, max(len(column.cards)-1, 0))
		t.scrollToCursor()
		return nil, true

	case vortexTickMsg:
		t.turning = false
		t.turn++
		return t.turnVortex(), true

	case cardMoveFailedMsg:
		return tea.Batch(
			notifyError("Could not move the card", msg.err),
			t.reread(msg.from),
			t.reread(msg.to),
		), true
	}
	return nil, false
}

// adopt carries what the reader arranged over to a table that has just been read
// again: which columns they folded, and where they were standing in each one.
//
// The cards come along so the board does not blink, but nothing is marked read:
// every open column is asked for again, and what comes back replaces what is
// there.
func adopt(fresh, was []*cardColumn) []*cardColumn {
	before := make(map[int64]*cardColumn, len(was))
	for _, column := range was {
		before[column.id] = column
	}
	for _, column := range fresh {
		if then, known := before[column.id]; known {
			column.collapsed = then.collapsed
			column.cursor, column.offset = then.cursor, then.offset
			column.cards = then.cards
			column.watching, column.knowsWatching = then.watching, then.knowsWatching
		}
	}
	return fresh
}

func (t *cardTableScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	// Walking back to a board does not open it again, so the spiral picks up on
	// the first key press instead. Nothing happens if one is already in flight.
	if resumed := t.turnVortex(); resumed != nil {
		return tea.Batch(resumed, t.handleBoardKey(msg))
	}
	return t.handleBoardKey(msg)
}

func (t *cardTableScreen) handleBoardKey(msg tea.KeyPressMsg) tea.Cmd {
	// A card in the air changes what every key means: the arrows carry it, and
	// enter puts it down. Nothing else on the board can be done one-handed while
	// holding something.
	if t.held != nil {
		return t.carryKey(msg)
	}

	switch key := msg.String(); key {
	case "left", "h":
		return t.step(-1)
	case "right", "l":
		return t.step(1)
	case "up", "k":
		t.moveCardCursor(-1)
		return nil
	case "down", "j":
		t.moveCardCursor(1)
		return nil
	case "g":
		t.jumpCardCursor(0)
		return nil
	case "G":
		t.jumpCardCursor(len(t.here().cards) - 1)
		return nil
	case collapseColumnKey:
		return t.toggleCollapsed()
	case moveCardKey:
		return t.pickUp()
	case addCardKey:
		return t.addCard()
	case addColumnKey:
		return t.addColumn()
	case editColumnKey:
		return t.editColumn()
	case watchColumnKey:
		return t.toggleWatch()
	case refreshBoardKey:
		return t.Init()
	}

	if msg.Key().Code == tea.KeyEnter {
		return t.open()
	}
	return nil
}

// here is the column the cursor is standing in. There is always one — a board
// with no columns answers with an empty one rather than nothing, so every caller
// can ask what is under the cursor without checking first.
func (t *cardTableScreen) here() *cardColumn {
	if t.cursor < 0 || t.cursor >= len(t.columns) {
		return &cardColumn{}
	}
	return t.columns[t.cursor]
}

func (t *cardTableScreen) columnByID(id int64) *cardColumn {
	for _, column := range t.columns {
		if column.id == id {
			return column
		}
	}
	return nil
}

// step walks sideways to the next column, and reads it if this is the first the
// reader has asked to see it.
func (t *cardTableScreen) step(by int) tea.Cmd {
	if len(t.columns) == 0 {
		return nil
	}
	t.cursor = max(min(t.cursor+by, len(t.columns)-1), 0)
	t.scrollToCursor()
	return t.readOpenColumns()
}

func (t *cardTableScreen) moveCardCursor(by int) {
	column := t.here()
	if len(column.cards) == 0 {
		return
	}
	column.cursor = max(min(column.cursor+by, len(column.cards)-1), 0)
	t.scrollToCursor()
}

func (t *cardTableScreen) jumpCardCursor(to int) {
	column := t.here()
	if len(column.cards) == 0 {
		return
	}
	column.cursor = max(min(to, len(column.cards)-1), 0)
	t.scrollToCursor()
}

// toggleCollapsed folds the column under the cursor away, or opens it back up
// and reads what is in it. A wormhole has nothing to open: it is a way off the
// board rather than a place cards sit.
func (t *cardTableScreen) toggleCollapsed() tea.Cmd {
	column := t.here()
	if column.kind == columnWormhole {
		return nil
	}
	column.collapsed = !column.collapsed
	t.scrollToCursor()
	return t.readOpenColumns()
}

// editColumn opens the form for everything the web's column menu does. There is
// nothing to rename or color about a wormhole: it is a way off the board.
func (t *cardTableScreen) editColumn() tea.Cmd {
	column := t.here()
	if column.id == 0 || column.kind == columnWormhole {
		return nil
	}
	edited, bucket := column, t.inside.id
	return func() tea.Msg { return editColumnMsg{column: edited, bucket: bucket} }
}

// columnSaved takes a rename or a recolor straight onto the board. The server
// has just handed both back, so there is nothing to ask for.
func (t *cardTableScreen) columnSaved(msg columnSavedMsg) tea.Cmd {
	column := t.columnByID(msg.column)
	if column == nil {
		return nil
	}
	column.title, column.color = msg.title, msg.color
	return notify("Saved " + msg.title)
}

// columnGone drops a column the reader archived or trashed, and stands the
// cursor on whatever fell into its place.
func (t *cardTableScreen) columnGone(msg columnGoneMsg) tea.Cmd {
	for at, column := range t.columns {
		if column.title != msg.title {
			continue
		}
		t.columns = slices.Delete(t.columns, at, at+1)
		t.clampCursor()
		break
	}
	return notify(msg.said + " " + msg.title)
}

// open is what enter does where the cursor is standing: the card under it, or —
// on a wormhole, which has no cards — the table at the far end.
func (t *cardTableScreen) open() tea.Cmd {
	column := t.here()
	if column.kind == columnWormhole {
		return t.travel(column)
	}
	if column.cursor >= len(column.cards) {
		return nil
	}
	chosen := column.cards[column.cursor]
	return func() tea.Msg { return openCardMsg{card: chosen} }
}

// travel goes through a wormhole to the table it comes out at.
//
// The far side is named by a column on another table, in another project, which
// is the same thing a pasted URL names — so this is the deep link's own walk:
// read up to the board, open the project, then the board. The trail comes back
// Home › That project › That table, because that is where the reader now is.
func (t *cardTableScreen) travel(hole *cardColumn) tea.Cmd {
	if !hole.linked || hole.destinationColumn == 0 {
		return notify("That wormhole doesn't go anywhere any more")
	}
	return tea.Batch(
		notify("Through the wormhole to "+hole.destination),
		resolveTarget(t.ctx.Ctx(), t.ctx.app, Target{
			ProjectID: hole.destinationBucket,
			Kind:      "columns",
			ID:        hole.destinationColumn,
		}),
	)
}

// --- Moving cards ---

// carried is a card the reader has picked up, and where they have carried it to.
//
// Nothing is written while it is in the air. Moving a card three columns along
// used to be three moves, because each step went to the server as it happened —
// and a card's own history then read "moved to Next, moved to In progress, moved
// to Needs code review" for one drag. Picking it up, carrying it, and putting it
// down is one move, which is what it was.
type carried struct {
	card card

	// from is the column it was lifted out of, and at is the one it is hovering
	// over now.
	from *cardColumn
	at   int
}

// pickUp lifts the card under the cursor off the board.
func (t *cardTableScreen) pickUp() tea.Cmd {
	from := t.here()
	if from.cursor >= len(from.cards) {
		return nil
	}

	lifted := from.cards[from.cursor]
	from.take(from.cursor)
	t.held = &carried{card: lifted, from: from, at: t.cursor}
	return notify("Carrying " + quoted(lifted.title) + " — arrows to move it, enter to drop it")
}

// carryKey is every key while a card is in the air: the arrows carry it, enter
// and m put it down, esc puts it back.
func (t *cardTableScreen) carryKey(msg tea.KeyPressMsg) tea.Cmd {
	switch key := msg.String(); key {
	case "left", "h":
		t.carry(-1)
		return nil
	case "right", "l":
		t.carry(1)
		return nil
	case moveCardKey:
		return t.drop()
	}

	switch msg.Key().Code {
	case tea.KeyEnter:
		return t.drop()
	case tea.KeyEscape:
		return t.putBack()
	}
	return nil
}

// carry walks the card sideways. Nothing goes over the wire: the board is
// showing where it would land, not where it has landed.
func (t *cardTableScreen) carry(by int) {
	t.held.at = max(min(t.held.at+by, len(t.columns)-1), 0)
	t.cursor = t.held.at
	t.scrollToCursor()
}

// drop puts the card down where it is hovering, which is the one move the whole
// carry adds up to.
//
// The board draws it before the server has agreed: a card that takes a second to
// land is a card the reader watches instead of a board they use.
// cardMoveFailedMsg is what puts it back.
func (t *cardTableScreen) drop() tea.Cmd {
	held := t.held
	t.held = nil

	to := t.columns[held.at]
	if to == held.from {
		// Put down where it was picked up. Nothing moved, so nothing is written.
		held.from.receive(held.card)
		t.scrollToCursor()
		return nil
	}

	if to.kind == columnWormhole {
		// Through a wormhole the card leaves this board altogether, so it does
		// not go into the strip it was dropped on.
		return tea.Batch(
			sendCard(t.ctx.Ctx(), t.ctx.app, held.card.id, to.id, held.from.id),
			notify("Sent "+quoted(held.card.title)+" through the wormhole to "+to.destination),
		)
	}

	to.receive(held.card)
	t.scrollToCursor()
	return tea.Batch(
		sendCard(t.ctx.Ctx(), t.ctx.app, held.card.id, to.id, held.from.id),
		notify("Moved "+quoted(held.card.title)+" to "+to.title),
	)
}

// putBack is esc: the card goes back where it came from and nothing was written,
// so there is nothing to undo.
func (t *cardTableScreen) putBack() tea.Cmd {
	held := t.held
	t.held = nil

	held.from.receive(held.card)
	t.cursor = t.indexOf(held.from.id)
	t.scrollToCursor()
	return notify("Left " + quoted(held.card.title) + " where it was")
}

// HandleBack is esc while a card is in the air. The board keeps it rather than
// letting the workspace pop the screen out from under a move in progress.
func (t *cardTableScreen) HandleBack() bool {
	if t.held == nil {
		return false
	}
	t.putBack()
	return true
}

// quoted wraps a title for a sentence in a toast, cutting it where a toast has
// room rather than letting a card named a paragraph fill the screen.
func quoted(title string) string {
	return "“" + truncateToWidth(title, 40) + "”"
}

// take lifts a card out of the column and leaves the cursor on whatever fell
// into its place.
func (c *cardColumn) take(at int) {
	c.cards = slices.Delete(c.cards, at, at+1)
	c.count = max(c.count-1, 0)
	c.cursor = max(min(at, len(c.cards)-1), 0)
}

// receive puts a card at the top of the column, which is where Basecamp puts a
// moved card when nobody says otherwise, and stands the cursor on it.
func (c *cardColumn) receive(moved card) {
	c.count++
	if !c.loaded {
		// Nothing has been read here, so there is no list to put it in — the
		// count is the whole story until the column is opened.
		return
	}
	c.cards = slices.Insert(c.cards, 0, moved)
	c.cursor = 0
}

// --- Layout ---

// The room a column takes. A card's title has to wrap somewhere a reader can
// follow, and much under twenty-four columns puts one word on a line.
const (
	minColumnWidth = 24
	maxColumnWidth = 38

	// A collapsed column is wide enough for its count in brackets, which is the
	// widest thing written in it.
	collapsedColumnWidth = 5

	// A wormhole is wide enough to write where it comes out, which is a project,
	// a table and a column. Narrower than a column, because it holds no cards.
	wormholeColumnWidth = 18

	// The untinted space between two columns, which is what keeps two washes
	// from reading as one.
	columnGap = 1
)

// widths is how wide each column is drawn, in board order.
//
// A collapsed column takes a strip and a wormhole takes what it needs to say
// where it goes; both are fixed. What is left over is split between the columns
// that hold cards, so a board that fits fills the width rather than leaving a
// gutter down the right — and one that does not falls back to a width a card is
// readable at and scrolls.
func (t *cardTableScreen) widths() []int {
	widths := make([]int, len(t.columns))
	open := 0
	fixed := max(len(t.columns)-1, 0) * columnGap
	for index, column := range t.columns {
		if held := column.fixedWidth(); held > 0 {
			widths[index] = held
			fixed += held
			continue
		}
		open++
	}
	if open == 0 {
		return widths
	}

	room := t.width - fixed
	each := max(min(room/open, maxColumnWidth), minColumnWidth)

	// Whatever the division left over goes to the columns on the left, a cell
	// each, so a board that fits reaches the right edge instead of stopping a
	// few cells short of it.
	over := 0
	if each*open < room && each < maxColumnWidth {
		over = room - each*open
	}
	for index, column := range t.columns {
		if column.fixedWidth() > 0 {
			continue
		}
		widths[index] = each
		if over > 0 {
			widths[index]++
			over--
		}
	}
	return widths
}

// fixedWidth is the room a column takes regardless of what the board has, and
// zero for one that shares out whatever is left.
func (c *cardColumn) fixedWidth() int {
	switch {
	case c.kind == columnWormhole:
		return wormholeColumnWidth
	case c.collapsed:
		return collapsedColumnWidth
	default:
		return 0
	}
}

// folded is whether the column is drawn as a strip. A wormhole always is: it
// holds nothing, and a full column that is always empty is a hole in the board.
func (c *cardColumn) folded() bool { return c.collapsed || c.kind == columnWormhole }

// paint is the color the column is drawn in, by the name Basecamp stores.
//
// The two columns Basecamp puts at the end of every board have none of their
// own — nobody picked one — and the web paints them anyway: Done is green
// because it means finished, and Not now the neutral gray because it means set
// aside. Only a column somebody made is ever uncolored.
func (c *cardColumn) paint() string {
	switch {
	case c.color != "":
		return c.color
	case c.kind == columnDone:
		return "green"
	case c.kind == columnNotNow:
		return "gray"
	default:
		return ""
	}
}

// visible is the run of columns the board draws, starting at first, and where
// the run stops. A column half off the right edge is still drawn — that is what
// says there is more board over there.
func (t *cardTableScreen) visible(widths []int) (from, to int) {
	room := t.width
	from = min(t.first, max(len(t.columns)-1, 0))
	for at := from; at < len(t.columns); at++ {
		if room <= 0 {
			return from, at
		}
		room -= widths[at] + columnGap
	}
	return from, len(t.columns)
}

// scrollToCursor brings the cursor's column onto the board, and its card onto
// the column.
func (t *cardTableScreen) scrollToCursor() {
	t.scrollColumnsToCursor()
	t.scrollCardsToCursor()
}

func (t *cardTableScreen) scrollColumnsToCursor() {
	if len(t.columns) == 0 || t.width <= 0 {
		t.first = 0
		return
	}

	t.first = min(t.first, t.cursor)

	// Walk the left edge right until the cursor's column fits whole. Anything
	// that scrolls off the front the reader has already seen.
	widths := t.widths()
	for t.first < t.cursor {
		room := t.width
		for at := t.first; at <= t.cursor; at++ {
			room -= widths[at] + columnGap
		}
		if room >= -columnGap {
			break
		}
		t.first++
	}
}

func (t *cardTableScreen) scrollCardsToCursor() {
	column := t.here()
	if t.cardsHeight() <= 0 || len(column.cards) == 0 {
		column.offset = 0
		return
	}

	width := t.widths()[min(t.cursor, max(len(t.columns)-1, 0))]
	rows := t.cardRows(column, t.paintFor(column, true), width, true)

	// A card is several rows and the cursor stands on all of them, so scrolling
	// to its first row is not enough: its last one has to land on screen too, or
	// the reader is looking at a title with no byline.
	first, last := -1, -1
	for index, row := range rows {
		if row.card == column.cursor {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first < 0 {
		column.offset = 0
		return
	}

	room := t.cardRoom(rows)
	column.offset = min(column.offset, first)
	if last >= column.offset+room {
		column.offset = last - room + 1
	}
	column.offset = max(min(column.offset, max(len(rows)-room, 0)), 0)
}

func (t *cardTableScreen) clampCursor() {
	t.cursor = max(min(t.cursor, len(t.columns)-1), 0)
	t.scrollToCursor()
}

// HelpBindings says what the keys do, which is not the same thing while a card
// is in the air: there is one thing to do with it and two ways to stop.
func (t *cardTableScreen) HelpBindings() []helpBinding {
	if t.held != nil {
		return []helpBinding{
			{"←→", "carry it"},
			{"enter", "drop it here"},
			{"esc", "leave it where it was"},
		}
	}
	return []helpBinding{
		{"←→", "column"},
		{"↑↓", "card"},
		{"enter", "open"},
		{moveCardKey, "move card"},
		{addCardKey, "add card"},
		{addColumnKey, "add column"},
		{editColumnKey, "edit column"},
		{watchColumnKey, "watch"},
		{collapseColumnKey, "fold"},
	}
}

// --- Reading ---

// readOpenColumns asks for the cards in every column that is open and has not
// been read yet.
//
// A collapsed column is not read at all. That is most of what makes the board
// cheap to open: Not now and Done arrive folded, and Done on a board that has
// been running a year holds more cards than the rest of the table put together.
func (t *cardTableScreen) readOpenColumns() tea.Cmd {
	var reads []tea.Cmd
	for _, column := range t.columns {
		if column.folded() || column.loaded || column.reading {
			continue
		}
		column.reading = true
		t.pending++
		reads = append(reads, loadColumnCards(t.ctx.Ctx(), t.ctx.app, column.id))
	}
	return tea.Batch(reads...)
}

// reread reads one column again, for when a write the board had already drawn
// turns out not to have landed.
func (t *cardTableScreen) reread(id int64) tea.Cmd {
	column := t.columnByID(id)
	if column == nil || column.folded() {
		return nil
	}
	column.reading = true
	t.pending++
	return loadColumnCards(t.ctx.Ctx(), t.ctx.app, id)
}

func loadCardTable(ctx context.Context, app *appctx.App, tableID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return cardTableLoadedMsg{err: err}
		}
		table, err := app.Account().CardTables().Get(ctx, tableID)
		if err != nil {
			return cardTableLoadedMsg{err: err}
		}
		return cardTableLoadedMsg{columns: toColumns(*table)}
	}
}

func loadColumnCards(ctx context.Context, app *appctx.App, columnID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return cardsLoadedMsg{column: columnID, err: err}
		}
		result, err := app.Account().Cards().List(ctx, columnID, &basecamp.CardListOptions{
			Limit: cardPageSize,
		})
		if err != nil {
			return cardsLoadedMsg{column: columnID, err: err}
		}

		cards := make([]card, 0, len(result.Cards))
		for _, found := range result.Cards {
			cards = append(cards, toCard(found))
		}
		return cardsLoadedMsg{column: columnID, cards: cards}
	}
}

// sendCard moves a card to another column, and answers only when it did not
// work: the board has already drawn the move, so there is nothing for a
// successful answer to say.
//
// A wormhole's id is a column id as far as this is concerned. Moving onto one
// teleports the card to a column on another table, which is the only way a card
// crosses projects.
func sendCard(ctx context.Context, app *appctx.App, cardID, to, from int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return cardMoveFailedMsg{from: from, to: to, err: err}
		}
		if err := app.Account().Cards().Move(ctx, cardID, to, nil); err != nil {
			return cardMoveFailedMsg{from: from, to: to, err: err}
		}
		return nil
	}
}

// toColumns puts a table's lists in the order the web draws them: Triage, then
// the columns the reader made, then the two Basecamp pins to the right edge, and
// the wormholes off the end.
//
// The API answers in the order Kanban::Board#kanban_lists builds — Triage, Not
// now, the columns, Done — which is the order they were created rather than the
// order anybody sees them in.
func toColumns(table basecamp.CardTable) []*cardColumn {
	columns := make([]*cardColumn, 0, len(table.Lists)+len(table.Wormholes))
	for _, list := range table.Lists {
		columns = append(columns, toColumn(list))
	}
	slices.SortStableFunc(columns, func(a, b *cardColumn) int { return a.rank() - b.rank() })

	for _, hole := range table.Wormholes {
		columns = append(columns, toWormhole(hole))
	}
	return columns
}

func toColumn(list basecamp.CardColumn) *cardColumn {
	kind := columnKinds[list.Type]
	column := &cardColumn{
		id:    list.ID,
		title: richtext.SanitizeSingleLine(list.Title),
		color: list.Color,
		kind:  kind,
		count: list.CardsCount,

		// Not now and Done are where cards go to stop being looked at, so the
		// web keeps them folded against the right edge and so does this.
		collapsed: kind == columnNotNow || kind == columnDone,
	}
	if list.OnHold != nil {
		column.onHold = list.OnHold.CardsCount
	}
	return column
}

// toWormhole reads a wormhole as the column it leads to. Its title is the whole
// path to the far side — project, table, column — which is more than a strip has
// room for, so the strip is named after the column and the toast says the rest.
func toWormhole(hole basecamp.Wormhole) *cardColumn {
	destination := richtext.SanitizeSingleLine(hole.Title)
	column := &cardColumn{
		id:          hole.ID,
		title:       lastPathSegment(destination),
		kind:        columnWormhole,
		linked:      hole.Linked,
		destination: destination,
	}
	if hole.Color != nil {
		column.color = *hole.Color
	}

	// DestinationURL is the only field naming the far side, and it names a
	// column rather than the table it is on — so going through one is a read up
	// to the board, which is what travel does with these.
	if hole.DestinationURL != nil {
		if parsed := urlarg.Parse(*hole.DestinationURL); parsed != nil {
			column.destinationColumn, _ = strconv.ParseInt(parsed.RecordingID, 10, 64)
			column.destinationBucket, _ = strconv.ParseInt(parsed.ProjectID, 10, 64)
		}
	}
	return column
}

// lastPathSegment is the far end of a wormhole's title, which Kanban::Wormhole
// words as "Project › Card table › Column".
func lastPathSegment(path string) string {
	if _, last, found := strings.Cut(path, breadcrumbSeparator); found {
		return lastPathSegment(last)
	}
	if path == "" {
		return "Wormhole"
	}
	return path
}

// rank is where a kind of column sits on the board, left to right.
func (c *cardColumn) rank() int {
	switch c.kind {
	case columnTriage:
		return 0
	case columnRegular:
		return 1
	case columnNotNow:
		return 2
	case columnDone:
		return 3
	default:
		return 4
	}
}

func toCard(found basecamp.Card) card {
	// BC5 renamed the field; older responses send only the one.
	markup := found.Description
	if markup == "" {
		markup = found.Content
	}

	out := card{
		id:       found.ID,
		title:    richtext.SanitizeSingleLine(found.Title),
		words:    newBody(markup, found.DescriptionAttachments),
		comments: found.CommentsCount,
		at:       found.CreatedAt.Local(),
	}
	if found.Creator != nil {
		out.author = richtext.SanitizeSingleLine(found.Creator.Name)
		out.avatar = found.Creator.AvatarURL
	}
	for _, who := range found.Assignees {
		out.assignees = append(out.assignees, richtext.SanitizeSingleLine(who.Name))
	}
	if len(found.Assignees) > 0 {
		// The picture beside a card is whoever is holding it, which is what the
		// web puts in the corner of one.
		out.avatar = found.Assignees[0].AvatarURL
	}

	// A card's steps are its own to-do list, and how far down it the work has
	// got is the one number the web puts on the face of the card.
	out.steps = len(found.Steps)
	for _, step := range found.Steps {
		out.stepList = append(out.stepList, toStep(step))
		if step.Completed {
			out.stepsDone++
		}
	}
	return out
}

func toStep(step basecamp.CardStep) cardStep {
	out := cardStep{
		title: richtext.SanitizeSingleLine(step.Title),
		done:  step.Completed,
	}
	if len(step.Assignees) > 0 {
		out.who = strings.Join(shortNames(peopleNames(step.Assignees)), ", ")
	}
	return out
}

func peopleNames(people []basecamp.Person) []string {
	names := make([]string, 0, len(people))
	for _, who := range people {
		names = append(names, richtext.SanitizeSingleLine(who.Name))
	}
	return names
}

// --- Rendering ---

func (t *cardTableScreen) View() string {
	if t.notice != "" {
		return strings.Join(wrapText(t.notice, t.width), "\n")
	}
	if len(t.columns) == 0 {
		if t.pending > 0 {
			return t.ctx.Styles().Muted.Render("Loading…")
		}
		return t.ctx.Styles().Muted.Render("This card table has no columns.")
	}

	widths := t.widths()
	from, to := t.visible(widths)

	drawn := make([]string, 0, to-from)
	for at := from; at < to; at++ {
		drawn = append(drawn, t.renderColumn(t.columns[at], widths[at], at == t.cursor))
	}

	// The last column is drawn whole and then cut at the edge, so a board with
	// more of it off to the right ends mid-column — which is what says so. Cut
	// after joining rather than before: it is the join that decides which cells
	// fall past the edge.
	board := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, t.spaced(drawn)...), "\n")
	for index, row := range board {
		board[index] = ansi.Truncate(row, t.width, "")
	}
	return strings.Join(board, "\n")
}

// spaced puts the gap columns in between the drawn ones. The gap is plain
// background: it is the space a colored column's wash stops at.
func (t *cardTableScreen) spaced(columns []string) []string {
	if len(columns) == 0 {
		return columns
	}
	gap := strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", columnGap)+"\n", t.height), "\n")

	spaced := make([]string, 0, 2*len(columns)-1)
	for index, column := range columns {
		if index > 0 {
			spaced = append(spaced, gap)
		}
		spaced = append(spaced, column)
	}
	return spaced
}
