package workspace

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// The keys inside the edit form: one to put the column away, one to throw it
// out, and one to park what is in it.
const (
	archiveColumnKey = "A"
	trashColumnKey   = "T"
	onHoldColumnKey  = "o"
)

// columnColors is the palette in the order the web's picker lays it out, with
// white first because white is no color at all. See Colored::ORDERED_COLORS.
var columnColors = []string{
	"white", "yellow", "orange", "red", "pink", "purple", "blue", "green", "brown", "gray",
}

// editColumnMsg asks the model to open the edit form over the board. The bucket
// comes along because two of the endpoints behind this form are addressed by
// project rather than by recording.
type editColumnMsg struct {
	column *cardColumn
	bucket int64
}

// columnSavedMsg is a rename or a recolor coming back, and columnGoneMsg a
// column archived or trashed. Both reach the board through the model: the modal
// is over the board, not part of it.
type columnSavedMsg struct {
	column int64
	title  string
	color  string
	err    error
}

type columnGoneMsg struct {
	title string
	said  string
	err   error
}

// columnOnHoldMsg is the parked list turned on or off.
type columnOnHoldMsg struct {
	column int64
	onHold bool
	err    error
}

// columnEdit is everything the web's column menu does, on one screen: rename it,
// color it, give it a place to park cards, put it away, throw it out.
//
// The web hides all of this behind a "…" on the column. A terminal has no menu
// to drop, and a menu of five things where four of them are one keystroke is
// four keystrokes too many — so the form shows them all and each has its key.
type columnEdit struct {
	ctx    *Context
	column *cardColumn
	bucket int64

	name  textinput.Model
	color int

	// onColors is whether the arrows walk the palette or move the cursor out of
	// the name field. The name is what opens focused, because renaming is what
	// this is usually opened for.
	onColors bool

	// confirming is which of the two undoable-with-difficulty actions has been
	// pressed once already, and is empty when neither has.
	confirming string

	saving bool
	notice string

	width int
}

func newColumnEdit(ctx *Context, msg editColumnMsg) *columnEdit {
	name := textinput.New()
	name.Prompt = ""
	name.SetValue(msg.column.title)
	name.CursorEnd()

	return &columnEdit{
		ctx:    ctx,
		column: msg.column,
		bucket: msg.bucket,
		name:   name,
		color:  indexOfColor(msg.column.paintedAs()),
	}
}

func indexOfColor(named string) int {
	for at, color := range columnColors {
		if color == named {
			return at
		}
	}
	return 0
}

func (e *columnEdit) Init() tea.Cmd { return e.name.Focus() }

func (e *columnEdit) Title() string { return "Edit " + e.column.title }

func (e *columnEdit) Resize(width, _ int) {
	e.width = width
	e.name.SetWidth(max(width, 1))
}

// Update only ever sees a write that failed. The model takes the ones that land:
// every one of them changes the board this form is standing over.
func (e *columnEdit) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case columnSavedMsg:
		e.saving = false
		e.notice = errorNotice("Could not save the column", msg.err)
		return nil, true

	case columnGoneMsg:
		e.saving = false
		e.notice = errorNotice("Could not "+strings.ToLower(msg.said)+" the column", msg.err)
		return nil, true

	case columnOnHoldMsg:
		e.saving = false
		e.notice = errorNotice("Could not change the parked list", msg.err)
		return nil, true
	}

	name, cmd := e.name.Update(msg)
	e.name = name
	return cmd, false
}

func (e *columnEdit) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	// Anything but a second press of the same key is a change of mind.
	pressed := e.confirming
	e.confirming = ""

	switch msg.Key().Code {
	case tea.KeyEsc:
		return nil, false
	case tea.KeyEnter:
		cmd, done := e.save()
		return cmd, !done
	case tea.KeyTab, tea.KeyDown, tea.KeyUp:
		return e.swap(), true
	case tea.KeyLeft, tea.KeyRight:
		if e.onColors {
			e.walkColors(msg.Key().Code)
			return nil, true
		}
	}

	// The letters are shortcuts only while the name is not being typed into.
	// There is no way for a form to tell an A meant as "archive" from an A meant
	// as the first letter of a name, so the palette is where they work.
	if e.onColors {
		switch msg.String() {
		case onHoldColumnKey:
			return e.toggleOnHold(), true
		case archiveColumnKey:
			return e.confirm(pressed, archiveColumnKey, e.archive)
		case trashColumnKey:
			return e.confirm(pressed, trashColumnKey, e.trash)
		}
		return nil, true
	}

	name, cmd := e.name.Update(msg)
	e.name = name
	return cmd, true
}

// confirm takes the second press of a key that means it, and arms the first.
func (e *columnEdit) confirm(pressed, key string, act func() tea.Cmd) (tea.Cmd, bool) {
	if pressed == key {
		return act(), true
	}
	e.confirming = key
	return nil, true
}

func (e *columnEdit) swap() tea.Cmd {
	e.onColors = !e.onColors
	if e.onColors {
		e.name.Blur()
		return nil
	}
	return e.name.Focus()
}

func (e *columnEdit) walkColors(code rune) {
	by := 1
	if code == tea.KeyLeft {
		by = -1
	}
	e.color = (e.color + by + len(columnColors)) % len(columnColors)
}

// save writes the name and the color together, and answers whether the form is
// finished. Nothing moved is nothing to write, so enter on an untouched form is
// a way out rather than a request.
func (e *columnEdit) save() (tea.Cmd, bool) {
	named := strings.TrimSpace(e.name.Value())
	if named == "" {
		e.notice = "A column needs a name."
		return nil, false
	}

	chosen := columnColors[e.color]
	if named == e.column.title && chosen == e.column.paintedAs() {
		return nil, true
	}

	e.saving = true
	e.notice = ""
	return saveColumn(e.ctx.Ctx(), e.ctx.app, e.column.id, e.bucket, named, chosen), false
}

func (e *columnEdit) toggleOnHold() tea.Cmd {
	e.saving = true
	e.notice = ""
	return holdColumn(e.ctx.Ctx(), e.ctx.app, e.column.id, e.bucket, e.column.onHold > 0)
}

func (e *columnEdit) archive() tea.Cmd {
	e.saving = true
	e.notice = ""
	return archiveColumn(e.ctx.Ctx(), e.ctx.app, e.column.id, e.column.title)
}

func (e *columnEdit) trash() tea.Cmd {
	e.saving = true
	e.notice = ""
	return trashColumn(e.ctx.Ctx(), e.ctx.app, e.column.id, e.column.title)
}

// --- Rendering ---

func (e *columnEdit) View() string {
	styles := e.ctx.Styles()
	inner := max(e.width, 1)

	lines := []string{
		styles.Muted.Render("Name"),
		e.name.View(),
		"",
		styles.Muted.Render("Color"),
		e.swatches(),
		lipgloss.NewStyle().Foreground(styles.Theme().Border).Render(strings.Repeat("─", inner)),
		"",
		e.onHoldRow(),
	}

	if e.saving {
		lines = append(lines, "", styles.Muted.Render("Saving…"))
	}
	if e.notice != "" {
		lines = append(lines, "")
		lines = append(lines, wrapText(e.notice, e.width)...)
	}
	return strings.Join(append(lines, "", e.dangerRow()), "\n")
}

// swatches is the palette, with the chosen one in brackets. White is the default
// and shows as a struck-through ring rather than a white dot, which is what the
// web's picker does with it: it means no color, not the color white.
func (e *columnEdit) swatches() string {
	styles := e.ctx.Styles()
	theme := styles.Theme()

	drawn := make([]string, 0, len(columnColors))
	for at, named := range columnColors {
		glyph := "⬤"
		mark := lipgloss.NewStyle()
		if tone, painted := theme.CardColor(named); painted {
			mark = mark.Foreground(tone)
		} else {
			glyph = "⊘"
			mark = styles.Muted
		}

		swatch := mark.Render(glyph)
		if at == e.color {
			swatch = lipgloss.NewStyle().Foreground(theme.Primary).Render("[") +
				swatch + lipgloss.NewStyle().Foreground(theme.Primary).Render("]")
		} else {
			swatch = " " + swatch + " "
		}
		drawn = append(drawn, swatch)
	}

	line := strings.Join(drawn, "")
	if !e.onColors {
		return line
	}
	room := max(e.width-tui.DisplayWidth(line), 0)
	return line + styles.Muted.Render(truncateToWidth("  ←→ to pick "+columnColors[e.color], room))
}

// onHoldRow is the parked list: a place inside the column for cards that are
// waiting on something. The web calls it "On hold".
func (e *columnEdit) onHoldRow() string {
	styles := e.ctx.Styles()
	if e.column.onHold > 0 {
		return styles.Muted.Render(onHoldColumnKey + " to turn off 'On hold' · " +
			e.column.held() + " parked")
	}
	return styles.Muted.Render(onHoldColumnKey + " to enable 'On hold'")
}

// dangerRow is the two ways out, and the second press that means either of them.
func (e *columnEdit) dangerRow() string {
	styles := e.ctx.Styles()
	loud := lipgloss.NewStyle().Foreground(styles.Theme().Error).Bold(true)

	switch e.confirming {
	case archiveColumnKey:
		return loud.Render("Press " + archiveColumnKey + " again to archive “" + e.column.title +
			"”. Its cards go with it.")
	case trashColumnKey:
		return loud.Render("Press " + trashColumnKey + " again to trash “" + e.column.title +
			"”. Its cards go with it.")
	}
	if !e.onColors {
		return styles.Muted.Render("tab for the color and the rest")
	}
	return styles.Muted.Render(archiveColumnKey + " to archive · " + trashColumnKey + " to trash")
}

func (e *columnEdit) HelpBindings() []helpBinding {
	if e.onColors {
		return []helpBinding{
			{"←→", "color"},
			{"enter", "save"},
			{onHoldColumnKey, "on hold"},
			{archiveColumnKey, "archive"},
			{trashColumnKey, "trash"},
			{"esc", "cancel"},
		}
	}
	return []helpBinding{{"enter", "save"}, {"tab", "color"}, {"esc", "cancel"}}
}

// paintedAs is the column's color as the picker names it. Basecamp stores no
// color for a column nobody painted, and the picker's first swatch is what that
// means.
func (c *cardColumn) paintedAs() string {
	if c.color == "" {
		return "white"
	}
	return c.color
}

// --- Writing ---

// saveColumn writes the name and the color, which are two endpoints: Update
// takes a title and a description, and the color has its own.
func saveColumn(ctx context.Context, app *appctx.App, columnID, bucketID int64, name, color string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return columnSavedMsg{column: columnID, err: err}
		}
		if _, err := app.Account().CardColumns().Update(ctx, columnID,
			&basecamp.UpdateColumnRequest{Title: name}); err != nil {
			return columnSavedMsg{column: columnID, err: err}
		}
		if _, err := app.Account().CardColumns().SetColor(ctx, bucketID, columnID, color); err != nil {
			return columnSavedMsg{column: columnID, err: err}
		}
		return columnSavedMsg{column: columnID, title: name, color: color}
	}
}

func holdColumn(ctx context.Context, app *appctx.App, columnID, bucketID int64, on bool) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return columnOnHoldMsg{column: columnID, err: err}
		}
		if on {
			if _, err := app.Account().CardColumns().DisableOnHold(ctx, bucketID, columnID); err != nil {
				return columnOnHoldMsg{column: columnID, err: err}
			}
			return columnOnHoldMsg{column: columnID, onHold: false}
		}
		if _, err := app.Account().CardColumns().EnableOnHold(ctx, bucketID, columnID); err != nil {
			return columnOnHoldMsg{column: columnID, err: err}
		}
		return columnOnHoldMsg{column: columnID, onHold: true}
	}
}

func archiveColumn(ctx context.Context, app *appctx.App, columnID int64, title string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return columnGoneMsg{title: title, said: "archive", err: err}
		}
		if err := app.Account().Recordings().Archive(ctx, columnID); err != nil {
			return columnGoneMsg{title: title, said: "archive", err: err}
		}
		return columnGoneMsg{title: title, said: "Archived"}
	}
}

func trashColumn(ctx context.Context, app *appctx.App, columnID int64, title string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return columnGoneMsg{title: title, said: "trash", err: err}
		}
		if err := app.Account().Recordings().Trash(ctx, columnID); err != nil {
			return columnGoneMsg{title: title, said: "trash", err: err}
		}
		return columnGoneMsg{title: title, said: "Trashed"}
	}
}
