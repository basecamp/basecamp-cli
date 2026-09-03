package workspace

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// The mark on a column the reader is watching. The web says "Watching:" and
// shows the faces; a strip two dozen cells wide has room for the fact.
const watchedGlyph = "⦿"

// addCardMsg and addColumnMsg ask the model to open the form over the board.
// Both carry where the new thing goes: a card into a column, a column onto the
// table.
type addCardMsg struct {
	column int64
	into   string
}

type addColumnMsg struct{ table int64 }

// cardAddedMsg and columnAddedMsg are the writes coming back, on their way to
// the board that has to show them.
type cardAddedMsg struct {
	column int64
	card   card
	err    error
}

type columnAddedMsg struct {
	title string
	err   error
}

// columnWatchMsg is a column's watching turned on or off.
type columnWatchMsg struct {
	column   int64
	watching bool
	err      error
}

// --- Watching a column ---

// toggleWatch turns watching the column under the cursor on or off.
//
// The board is not told which way round it is. A column's payload lists who is
// subscribed and never says whether that includes the reader, so the first w on
// a column is two round trips — ask, then flip — and every one after it is a
// single one, because the answer is kept.
func (t *cardTableScreen) toggleWatch() tea.Cmd {
	column := t.here()
	if column.id == 0 || column.kind == columnWormhole {
		return nil
	}
	return watchColumn(t.ctx.Ctx(), t.ctx.app, column.id, column.watching, column.knowsWatching)
}

func (t *cardTableScreen) watchChanged(msg columnWatchMsg) tea.Cmd {
	column := t.columnByID(msg.column)
	if column == nil {
		return nil
	}
	if msg.err != nil {
		return notifyError("Could not change watching "+column.title, msg.err)
	}

	column.watching, column.knowsWatching = msg.watching, true
	if msg.watching {
		return notify("Watching " + column.title)
	}
	return notify("Stopped watching " + column.title)
}

func watchColumn(ctx context.Context, app *appctx.App, columnID int64, watching, known bool) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return columnWatchMsg{column: columnID, err: err}
		}

		if !known {
			found, err := app.Account().Subscriptions().Get(ctx, columnID)
			if err != nil {
				return columnWatchMsg{column: columnID, err: err}
			}
			watching = found.Subscribed
		}

		if watching {
			if err := app.Account().CardColumns().Unwatch(ctx, columnID); err != nil {
				return columnWatchMsg{column: columnID, err: err}
			}
			return columnWatchMsg{column: columnID, watching: false}
		}

		if _, err := app.Account().CardColumns().Watch(ctx, columnID); err != nil {
			return columnWatchMsg{column: columnID, err: err}
		}
		return columnWatchMsg{column: columnID, watching: true}
	}
}

// --- Adding a card ---

// addCard opens the form for a new card in the column under the cursor. A
// wormhole is a way off the board rather than a place cards sit, so there is
// nowhere in it to put one.
func (t *cardTableScreen) addCard() tea.Cmd {
	column := t.here()
	if column.id == 0 || column.kind == columnWormhole {
		return nil
	}
	into, name := column.id, column.title
	return func() tea.Msg { return addCardMsg{column: into, into: name} }
}

// cardAdded puts a new card at the top of the column it was written into, which
// is where Basecamp puts one, and stands the cursor on it.
func (t *cardTableScreen) cardAdded(msg cardAddedMsg) tea.Cmd {
	column := t.columnByID(msg.column)
	if column == nil {
		return nil
	}
	column.receive(msg.card)
	if at := t.indexOf(msg.column); at >= 0 {
		t.cursor = at
	}
	t.scrollToCursor()
	return notify("Added " + quoted(msg.card.title))
}

func (t *cardTableScreen) indexOf(id int64) int {
	for at, column := range t.columns {
		if column.id == id {
			return at
		}
	}
	return -1
}

// cardForm writes one card: a title, and the first thing to say about it.
//
// The board's own form rather than the message board's: a card is a title and a
// note, and the composer behind a message is a full rich-text editor with a
// category picker and a draft button beside it.
type cardForm struct {
	ctx    *Context
	column int64
	into   string

	title textinput.Model
	note  textinput.Model

	// onNote is which of the two fields has the cursor. Two fields is too few
	// for a focus ring.
	onNote bool

	saving bool
	notice string

	width int
}

func newCardForm(ctx *Context, msg addCardMsg) *cardForm {
	title := textinput.New()
	title.Prompt = ""
	title.Placeholder = "What needs doing?"

	note := textinput.New()
	note.Prompt = ""
	note.Placeholder = "Anything to add (optional)"

	return &cardForm{ctx: ctx, column: msg.column, into: msg.into, title: title, note: note}
}

func (f *cardForm) Init() tea.Cmd { return f.title.Focus() }

func (f *cardForm) Title() string { return "New card in " + f.into }

func (f *cardForm) Resize(width, _ int) {
	f.width = width
	f.title.SetWidth(max(width, 1))
	f.note.SetWidth(max(width, 1))
}

// Update only ever sees a write that failed. The model takes one that landed:
// the card goes on the board, and the board is what is under this form.
func (f *cardForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	if added, ok := msg.(cardAddedMsg); ok {
		f.saving = false
		f.notice = errorNotice("Could not add the card", added.err)
		return nil, true
	}
	return f.blink(msg), false
}

// blink hands the message to whichever field has the cursor and lets it go on
// down: this form opens over a board that may still be reading, and claiming
// every message would leave those reads with nowhere to land.
func (f *cardForm) blink(msg tea.Msg) tea.Cmd {
	if f.onNote {
		note, cmd := f.note.Update(msg)
		f.note = note
		return cmd
	}
	title, cmd := f.title.Update(msg)
	f.title = title
	return cmd
}

func (f *cardForm) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.Key().Code {
	case tea.KeyEsc:
		return nil, false
	case tea.KeyTab, tea.KeyDown, tea.KeyUp:
		return f.swap(), true
	case tea.KeyEnter:
		return f.save(), true
	}
	return f.blink(msg), true
}

func (f *cardForm) swap() tea.Cmd {
	f.onNote = !f.onNote
	if f.onNote {
		f.title.Blur()
		return f.note.Focus()
	}
	f.note.Blur()
	return f.title.Focus()
}

func (f *cardForm) save() tea.Cmd {
	named := strings.TrimSpace(f.title.Value())
	if named == "" {
		f.notice = "A card needs a title."
		return nil
	}

	f.saving = true
	f.notice = ""
	return createCard(f.ctx.Ctx(), f.ctx.app, f.column, named, strings.TrimSpace(f.note.Value()))
}

func (f *cardForm) View() string {
	styles := f.ctx.Styles()

	lines := []string{
		styles.Muted.Render("Title"),
		f.title.View(),
		"",
		styles.Muted.Render("Note"),
		f.note.View(),
	}
	if f.saving {
		lines = append(lines, "", styles.Muted.Render("Adding…"))
	}
	if f.notice != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText(f.notice, f.width)...)
	}
	return strings.Join(lines, "\n")
}

func (f *cardForm) HelpBindings() []helpBinding {
	return []helpBinding{{"enter", "add"}, {"tab", "next field"}, {"esc", "cancel"}}
}

// --- Adding a column ---

func (t *cardTableScreen) addColumn() tea.Cmd {
	onto := t.board.id
	return func() tea.Msg { return addColumnMsg{table: onto} }
}

// columnForm writes one column: a name, and what it is for.
//
// No color. A new column arrives white, the way one does on the web, and e is
// where a color is chosen — which keeps the picker in one place rather than in
// two that have to agree.
type columnForm struct {
	ctx   *Context
	table int64

	name    textinput.Model
	about   textinput.Model
	onAbout bool

	saving bool
	notice string

	width int
}

func newColumnForm(ctx *Context, msg addColumnMsg) *columnForm {
	name := textinput.New()
	name.Prompt = ""
	name.Placeholder = "What is this column for?"

	about := textinput.New()
	about.Prompt = ""
	about.Placeholder = "A line about it (optional)"

	return &columnForm{ctx: ctx, table: msg.table, name: name, about: about}
}

func (f *columnForm) Init() tea.Cmd { return f.name.Focus() }

func (f *columnForm) Title() string { return "New column" }

func (f *columnForm) Resize(width, _ int) {
	f.width = width
	f.name.SetWidth(max(width, 1))
	f.about.SetWidth(max(width, 1))
}

// Update only ever sees a write that failed, for the same reason cardForm's
// does.
func (f *columnForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	if added, ok := msg.(columnAddedMsg); ok {
		f.saving = false
		f.notice = errorNotice("Could not add the column", added.err)
		return nil, true
	}
	return f.blink(msg), false
}

func (f *columnForm) blink(msg tea.Msg) tea.Cmd {
	if f.onAbout {
		about, cmd := f.about.Update(msg)
		f.about = about
		return cmd
	}
	name, cmd := f.name.Update(msg)
	f.name = name
	return cmd
}

func (f *columnForm) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.Key().Code {
	case tea.KeyEsc:
		return nil, false
	case tea.KeyTab, tea.KeyDown, tea.KeyUp:
		return f.swap(), true
	case tea.KeyEnter:
		return f.save(), true
	}
	return f.blink(msg), true
}

func (f *columnForm) swap() tea.Cmd {
	f.onAbout = !f.onAbout
	if f.onAbout {
		f.name.Blur()
		return f.about.Focus()
	}
	f.about.Blur()
	return f.name.Focus()
}

func (f *columnForm) save() tea.Cmd {
	named := strings.TrimSpace(f.name.Value())
	if named == "" {
		f.notice = "A column needs a name."
		return nil
	}

	f.saving = true
	f.notice = ""
	return createColumn(f.ctx.Ctx(), f.ctx.app, f.table, named, strings.TrimSpace(f.about.Value()))
}

func (f *columnForm) View() string {
	styles := f.ctx.Styles()

	lines := []string{
		styles.Muted.Render("Name"),
		f.name.View(),
		"",
		styles.Muted.Render("About"),
		f.about.View(),
	}
	if f.saving {
		lines = append(lines, "", styles.Muted.Render("Adding…"))
	}
	if f.notice != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText(f.notice, f.width)...)
	}
	return strings.Join(lines, "\n")
}

func (f *columnForm) HelpBindings() []helpBinding {
	return []helpBinding{{"enter", "add"}, {"tab", "next field"}, {"esc", "cancel"}}
}

// --- Writing ---

func createCard(ctx context.Context, app *appctx.App, columnID int64, title, note string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return cardAddedMsg{column: columnID, err: err}
		}
		written, err := app.Account().Cards().Create(ctx, columnID, &basecamp.CreateCardRequest{
			Title:   title,
			Content: note,
		})
		if err != nil {
			return cardAddedMsg{column: columnID, err: err}
		}
		return cardAddedMsg{column: columnID, card: toCard(*written)}
	}
}

func createColumn(ctx context.Context, app *appctx.App, tableID int64, name, about string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return columnAddedMsg{err: err}
		}
		if _, err := app.Account().CardColumns().Create(ctx, tableID, &basecamp.CreateColumnRequest{
			Title:       name,
			Description: about,
		}); err != nil {
			return columnAddedMsg{err: err}
		}
		return columnAddedMsg{title: name}
	}
}
