package workspace

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func fixedNow() time.Time {
	return time.Date(2026, 9, 3, 11, 30, 0, 0, time.UTC)
}

// TestTheBoardPutsBasecampsOwnColumnsWhereTheWebDoes reads a table shaped the
// way the API answers — Triage, Not now, the reader's columns, Done — and checks
// it comes back in the order the browser shows.
func TestTheBoardPutsBasecampsOwnColumnsWhereTheWebDoes(t *testing.T) {
	columns := toColumns(basecamp.CardTable{Lists: []basecamp.CardColumn{
		{ID: 1, Type: "Kanban::Triage", Title: "Triage", CardsCount: 7},
		{ID: 2, Type: "Kanban::NotNowColumn", Title: "Not now"},
		{ID: 3, Type: "Kanban::Column", Title: "Figuring it out", Color: "purple"},
		{ID: 4, Type: "Kanban::Column", Title: "In progress", Color: "orange", CardsCount: 2},
		{ID: 5, Type: "Kanban::DoneColumn", Title: "Done", CardsCount: 7},
	}})

	want := []string{"Triage", "Figuring it out", "In progress", "Not now", "Done"}
	got := make([]string, 0, len(columns))
	for _, column := range columns {
		got = append(got, column.title)
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("board reads %q, want %q", got, want)
	}

	// Not now and Done are where cards go to stop being looked at, so they
	// arrive folded and nothing reads them until the reader asks.
	for _, column := range columns {
		folded := column.kind == columnNotNow || column.kind == columnDone
		if column.collapsed != folded {
			t.Errorf("%s arrived collapsed=%v, want %v", column.title, column.collapsed, folded)
		}
	}
}

// TestAWormholeIsNamedAfterWhereItGoes: Kanban::Wormhole titles itself with the
// whole path to the far side, and the column it lands in is the last of it —
// which is what the wormhole is called, and what a toast about it says.
func TestAWormholeIsNamedAfterWhereItGoes(t *testing.T) {
	linked := true
	columns := toColumns(basecamp.CardTable{Wormholes: []basecamp.Wormhole{
		{ID: 9, Title: "HEY › Bugs › Inbox", Linked: linked},
	}})

	if len(columns) != 1 {
		t.Fatalf("read %d columns off a table with one wormhole", len(columns))
	}
	hole := columns[0]
	if hole.title != "Inbox" {
		t.Errorf("wormhole strip is named %q, want the destination column %q", hole.title, "Inbox")
	}
	if hole.destination != "HEY › Bugs › Inbox" {
		t.Errorf("wormhole leads to %q, want the whole path", hole.destination)
	}
	if !hole.folded() {
		t.Error("a wormhole holds nothing, so nothing should ever read it for cards")
	}
	if got := hole.fixedWidth(); got != wormholeColumnWidth {
		t.Errorf("the wormhole takes %d cells, want the %d it needs to say where it goes",
			got, wormholeColumnWidth)
	}
}

// TestACardCarriesItsStepsAndItsMarkup: a column's index renders the cards
// whole, so the screen behind one needs no read of its own.
func TestACardCarriesItsStepsAndItsMarkup(t *testing.T) {
	on := toCard(basecamp.Card{
		ID:            5,
		Title:         "Improve trial expiration flow",
		Description:   "<p>Something to <b>read</b></p>",
		CommentsCount: 5,
		Assignees:     []basecamp.Person{{Name: "Andy Didorosi"}},
		Steps: []basecamp.CardStep{
			{Title: "Draw it", Completed: true},
			{Title: "Ship it", Assignees: []basecamp.Person{{Name: "Jason Fried"}}},
		},
	})

	if on.steps != 2 || on.stepsDone != 1 {
		t.Errorf("card counts %d/%d steps, want 1/2", on.stepsDone, on.steps)
	}
	if len(on.words.parts) != 1 || !strings.Contains(on.words.parts[0].text, "**read**") {
		t.Errorf("card body reads %#v; the markup should arrive ready to draw", on.words.parts)
	}
	if got := on.progress(); got != "✓1/2 ●5" {
		t.Errorf("card face reads %q, want %q", got, "✓1/2 ●5")
	}
	if got := on.who(); got != "Andy D." {
		t.Errorf("card belongs to %q, want the assignee %q", got, "Andy D.")
	}
	if got := on.stepList[1].who; got != "Jason F." {
		t.Errorf("second step is on %q, want %q", got, "Jason F.")
	}
}

// TestACardsBylineKeepsItsBadges: the byline sheds the date, and then shortens
// the name, rather than cutting through what says how far along the card is.
func TestACardsBylineKeepsItsBadges(t *testing.T) {
	on := card{
		title:     "Improve trial expiration flow",
		assignees: []string{"Andy Didorosi"},
		steps:     4,
		stepsDone: 2,
		comments:  11,
		at:        fixedNow().AddDate(0, 0, -20),
	}

	for _, width := range []int{40, 24, 18, 12} {
		line := on.byline(fixedNow(), width)
		if got := tui.DisplayWidth(line); got > width {
			t.Errorf("byline at width %d came back %d cells wide: %q", width, got, line)
		}
		if !strings.Contains(line, "✓2/4") {
			t.Errorf("byline at width %d dropped the steps: %q", width, line)
		}
	}
}

// aBoard is the shape of the card table in the screenshots: an empty Triage, a
// run of colored columns, the two Basecamp folds against the right edge, and a
// wormhole off the end.
func aBoard(t *testing.T) *cardTableScreen {
	t.Helper()

	styles := tui.NewStylesWithTheme(tui.DefaultTheme(true))
	screen := newCardTable(&Context{styles: styles}, tool{id: 1, name: "Card Table"}, project{id: 9})
	screen.now = fixedNow

	made := func(at time.Duration) time.Time { return fixedNow().Add(-at) }
	screen.columns = []*cardColumn{
		{id: 10, title: "Triage", kind: columnTriage, loaded: true},
		{id: 11, title: "Later", kind: columnRegular, count: 3, loaded: true, cards: []card{
			{id: 100, title: "Plan checkout slide flow: hidden step stays tabbable after reload",
				author: "Michael Berger", at: made(21 * 24 * time.Hour), steps: 4},
			{id: 101, title: "Pricing a11y odds and ends",
				author: "Michael Berger", at: made(21 * 24 * time.Hour), steps: 8, stepsDone: 3},
			{id: 102, title: "Hitting people limit on FreeV2 should prompt to upgrade?",
				author: "Michael Berger", at: made(15 * 24 * time.Hour), comments: 2},
		}},
		{id: 12, title: "Next", color: "purple", kind: columnRegular, count: 2, loaded: true, cards: []card{
			{id: 110, title: "Update billing on a frozen account doesn't work",
				author: "Michael Berger", at: made(17 * 24 * time.Hour)},
			{id: 111, title: `"Back to work" on unauthenticated upgrade page`,
				assignees: []string{"Jason Fried"}, at: made(21 * 24 * time.Hour), comments: 2},
		}},
		{id: 13, title: "In progress", color: "orange", kind: columnRegular, count: 2, onHold: 1, loaded: true, cards: []card{
			{id: 120, title: "Improve trial expiration flow",
				assignees: []string{"Andy Didorosi"}, at: made(13 * time.Hour), comments: 5},
			{id: 121, title: "Intermittently can't click into the CC fields, fixed on reload",
				author: "Michael Berger", at: made(15 * 24 * time.Hour)},
		}},
		{id: 14, title: "Not now", kind: columnNotNow, count: 13, collapsed: true},
		{id: 15, title: "Done", kind: columnDone, count: 68, collapsed: true},
		{id: 16, title: "Inbox", kind: columnWormhole, destination: "HEY › Bugs › Inbox", linked: true},
	}
	return screen
}

// TestTheBoardDrawsEveryColumnTheSameHeight is the one thing the whole layout
// rests on: the columns are joined along their rows, so a column that comes back
// short or tall takes the board's rows with it.
func TestTheBoardDrawsEveryColumnTheSameHeight(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)

	view := screen.View()
	for index, line := range strings.Split(view, "\n") {
		if width := tui.DisplayWidth(line); width > 120 {
			t.Errorf("row %d is %d cells wide, past the 120 it was given", index, width)
		}
	}
	if rows := strings.Count(view, "\n") + 1; rows != 24 {
		t.Errorf("board drew %d rows, want 24", rows)
	}
}

// TestAFoldedColumnKeepsItsStrip checks the two columns Basecamp pins to the
// right edge arrive folded, and that a fold is narrow.
func TestAFoldedColumnKeepsItsStrip(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)

	widths := screen.widths()
	for index, column := range screen.columns {
		// A column that holds cards shares out whatever is left; a strip and a
		// wormhole each take the room they need and no more.
		if want := column.fixedWidth(); want > 0 {
			if widths[index] != want {
				t.Errorf("%s is %d cells wide, want its fixed %d", column.title, widths[index], want)
			}
			continue
		}
		if widths[index] < minColumnWidth {
			t.Errorf("%s is %d cells wide, under the %d a card reads at",
				column.title, widths[index], minColumnWidth)
		}
	}
}

// TestTheBoardScrollsSidewaysToTheCursor walks off the right edge and checks the
// board follows.
func TestTheBoardScrollsSidewaysToTheCursor(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(80, 24)

	for range len(screen.columns) {
		screen.HandleKey(keyPress("right"))
	}
	if screen.cursor != len(screen.columns)-1 {
		t.Fatalf("cursor stopped at %d, want %d", screen.cursor, len(screen.columns)-1)
	}

	widths := screen.widths()
	from, to := screen.visible(widths)
	if screen.cursor < from || screen.cursor >= to {
		t.Errorf("cursor at %d is off the drawn run %d..%d", screen.cursor, from, to)
	}
}

// TestCarryingACardWritesOneMove is the whole point of picking a card up rather
// than pushing it: the board shows it in the air the whole way across, and only
// putting it down is a move.
//
// A move per step wrote a move per step, and the card's own history then read
// "moved to Next, moved to In progress, moved to Needs code review" for what the
// reader did once.
func TestCarryingACardWritesOneMove(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.cursor = 1

	carried := screen.columns[1].cards[0]
	if cmd := screen.HandleKey(keyPress(moveCardKey)); cmd == nil {
		t.Fatal("m answered with nothing to do")
	}
	if screen.held == nil {
		t.Fatal("m picked nothing up")
	}
	if got := len(screen.columns[1].cards); got != 2 {
		t.Errorf("Later still holds %d cards, want 2 with one in the air", got)
	}

	// Two columns along, and not a single write on the way.
	for range 2 {
		if cmd := screen.HandleKey(keyPress("right")); cmd != nil {
			t.Error("carrying a card sent something to the server")
		}
	}
	if screen.held.at != 3 {
		t.Fatalf("the card is hovering over column %d, want 3", screen.held.at)
	}

	if cmd := screen.HandleKey(keyPress("enter")); cmd == nil {
		t.Fatal("dropping the card answered with nothing to do")
	}
	if screen.held != nil {
		t.Error("the card is still in the air after being dropped")
	}
	if got := screen.columns[3].cards[0].id; got != carried.id {
		t.Errorf("In progress leads with card %d, want the carried %d", got, carried.id)
	}
	if got := screen.columns[1].count; got != 2 {
		t.Errorf("Later still counts %d, want 2", got)
	}
}

// esc puts the card back where it came from, and nothing was written, so there
// is nothing to undo.
func TestEscapeLeavesACarriedCardWhereItWas(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.cursor = 1

	was := append([]card(nil), screen.columns[1].cards...)
	screen.HandleKey(keyPress(moveCardKey))
	screen.HandleKey(keyPress("right"))

	if !screen.HandleBack() {
		t.Fatal("esc while carrying a card was not claimed by the board")
	}
	if screen.held != nil {
		t.Error("the card is still in the air after esc")
	}
	if got := len(screen.columns[1].cards); got != len(was) {
		t.Errorf("Later came back with %d cards, want %d", got, len(was))
	}
	if got := screen.columns[1].cards[0].id; got != was[0].id {
		t.Errorf("Later leads with card %d, want the one that was put back, %d", got, was[0].id)
	}
	if screen.cursor != 1 {
		t.Errorf("the cursor stayed on column %d, want it back on 1 with the card", screen.cursor)
	}
}

// A card dropped where it was picked up moved nowhere, so nothing is written.
func TestDroppingACardWhereItWasWritesNothing(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.cursor = 1

	screen.HandleKey(keyPress(moveCardKey))
	if cmd := screen.HandleKey(keyPress("enter")); cmd != nil {
		t.Errorf("dropping a card where it started answered %T", cmd())
	}
	if got := len(screen.columns[1].cards); got != 3 {
		t.Errorf("Later came back with %d cards, want 3", got)
	}
}

// TestAWormholeTakesTheCardOffTheBoard: a card dropped on a wormhole lands on
// another table, so it does not turn up in the wormhole's own strip.
func TestAWormholeTakesTheCardOffTheBoard(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	// Done sits beside the wormhole, so a card in it is one step from going
	// through.
	done, hole := screen.columns[5], screen.columns[6]
	done.cards = []card{{id: 201, title: "Shipped"}}
	done.loaded, done.count = true, 1
	screen.cursor = 5

	screen.HandleKey(keyPress(moveCardKey))
	screen.HandleKey(keyPress("right"))
	if cmd := screen.HandleKey(keyPress("enter")); cmd == nil {
		t.Fatal("sending a card through the wormhole answered with nothing to do")
	}

	if got := len(done.cards); got != 0 {
		t.Errorf("Done kept %d cards, want none", got)
	}
	if got := len(hole.cards); got != 0 {
		t.Errorf("the wormhole is holding %d cards; it should hold none", got)
	}
	if got := hole.count; got != 0 {
		t.Errorf("the wormhole counts %d cards; it should count none", got)
	}
}

// A card in the air is drawn over the column it would land in, casting a shadow
// on it: that is what says it is above the board rather than in it.
func TestACarriedCardHoversWithAShadow(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.cursor = 1

	screen.HandleKey(keyPress(moveCardKey))
	screen.HandleKey(keyPress("right"))

	drawn := ansi.Strip(screen.View())
	if !strings.Contains(drawn, shadowUnderGlyph) || !strings.Contains(drawn, shadowEdgeGlyph) {
		t.Errorf("the carried card casts no shadow:\n%s", drawn)
	}
	if !strings.Contains(drawn, "Plan checkout") {
		t.Errorf("the carried card was not drawn over the column it is over:\n%s", drawn)
	}
}

// TestARecordedBoardParsesAndDraws feeds the board the payloads a real card
// table answers with — testdata recorded off the CLIs board — and draws it.
//
// The fixtures above say what the layout does; this says the layout survives
// what Basecamp actually sends: titles long enough to wrap past three lines,
// columns Basecamp made rather than a reader, a Triage full of cards, and
// descriptions carrying markup.
func TestARecordedBoardParsesAndDraws(t *testing.T) {
	var recorded basecamp.CardTable
	read(t, "testdata/cardtable.json", &recorded)

	var columns []struct {
		Column int64           `json:"column"`
		Cards  []basecamp.Card `json:"cards"`
	}
	read(t, "testdata/cards.json", &columns)

	screen := newCardTable(&Context{styles: tui.NewStylesWithTheme(tui.DefaultTheme(true))},
		tool{id: 10254012231, name: "Basecamp CLI"}, project{id: 48521764})
	screen.now = fixedNow
	screen.columns = toColumns(recorded)

	if len(screen.columns) != 5 {
		t.Fatalf("read %d columns off the recorded table, want 5", len(screen.columns))
	}

	for _, read := range columns {
		column := screen.columnByID(read.Column)
		if column == nil {
			t.Fatalf("no column %d on the recorded board", read.Column)
		}
		column.loaded = true
		column.collapsed = false
		for _, on := range read.Cards {
			column.cards = append(column.cards, toCard(on))
		}
	}

	for _, size := range [][2]int{{80, 24}, {120, 30}, {170, 42}, {40, 12}} {
		screen.Resize(size[0], size[1])
		view := screen.View()
		if rows := strings.Count(view, "\n") + 1; rows != size[1] {
			t.Errorf("at %dx%d the board drew %d rows", size[0], size[1], rows)
		}
		for index, line := range strings.Split(view, "\n") {
			if width := tui.DisplayWidth(line); width > size[0] {
				t.Errorf("at %dx%d row %d is %d cells wide", size[0], size[1], index, width)
			}
		}
	}

	// The screen behind a card is drawn from what the board already read, so a
	// card with a description has one without another request.
	triage := screen.columnByID(10254012232)
	behind := newCardScreen(screen.ctx, triage.cards[0])
	behind.now = fixedNow
	behind.Resize(80, 24)
	if behind.View() == "" {
		t.Error("the screen behind a recorded card drew nothing")
	}
}

func read(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// TestTheBoardsKeysOpenTheirForms walks the keys through the model rather than
// straight into the screen: the model claims a good many keys for itself before
// a screen sees one, and a binding that never arrives is a binding that does
// nothing.
func TestTheBoardsKeysOpenTheirForms(t *testing.T) {
	for _, want := range []struct{ key, modal string }{
		{addCardKey, "New card in Later"},
		{addColumnKey, "New column"},
		{editColumnKey, "Edit Later"},
	} {
		m := newTestModel(t)
		m.width, m.height = 140, 40
		m.nav.push(newCardTable(m.ctx, tool{id: 1, name: "Board"}, project{id: 2}))
		m.relayout()

		board, _ := m.nav.current().(*cardTableScreen)
		board.columns = []*cardColumn{{id: 10, title: "Later", kind: columnRegular, loaded: true}}

		pressed, cmd := m.handleKey(keyPress(want.key))
		if cmd == nil {
			t.Errorf("%q answered with no command", want.key)
			continue
		}
		after, _ := pressed.Update(cmd())

		settled, ok := after.(model)
		if !ok || settled.modal == nil {
			t.Errorf("%q left no form open", want.key)
			continue
		}
		if got := settled.modal.Title(); got != want.modal {
			t.Errorf("%q opened %q, want %q", want.key, got, want.modal)
		}
	}
}

// A wormhole says where it comes out — the project, the table, the column — the
// way the web writes it. A strip five cells wide could only say the last of
// those, which is the one thing a reader could have guessed.
func TestAWormholeWritesOutWhereItGoes(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.cursor = 6

	drawn := ansi.Strip(screen.View())
	for _, segment := range []string{"HEY", "Bugs", "Inbox"} {
		if !strings.Contains(drawn, segment) {
			t.Errorf("the wormhole does not say %q:\n%s", segment, drawn)
		}
	}
	if !strings.Contains(drawn, wormholeGlyph) {
		t.Errorf("the wormhole has no mark on it:\n%s", drawn)
	}
}

// enter on a wormhole travels: the far side is a column on another table in
// another project, which is the same walk a pasted URL takes.
func TestEnterOnAWormholeTravelsThroughIt(t *testing.T) {
	screen := aBoard(t)
	screen.Resize(120, 24)
	screen.cursor = 6

	hole := screen.columns[6]
	hole.destinationBucket, hole.destinationColumn = 777, 888

	if cmd := screen.HandleKey(keyPress("enter")); cmd == nil {
		t.Fatal("enter on a wormhole did nothing")
	}

	// An unlinked wormhole goes nowhere, and says so rather than reading.
	hole.linked = false
	cmd := screen.HandleKey(keyPress("enter"))
	if cmd == nil {
		t.Fatal("enter on a broken wormhole said nothing")
	}
	said, ok := cmd().(notifyMsg)
	if !ok || !strings.Contains(said.text, "doesn't go anywhere") {
		t.Errorf("a broken wormhole answered %v", cmd())
	}
}
