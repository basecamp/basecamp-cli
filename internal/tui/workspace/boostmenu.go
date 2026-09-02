package workspace

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// boostMenuMsg asks the model to open the boosts over one comment.
type boostMenuMsg struct {
	comment reply
	boosts  []boost
}

// boostMenu is every reaction on one comment, and what the reader may do about
// them.
//
// All of them, with whoever left each one named in full. The comment itself has
// only room for initials — a row of pills is what it is — so this is where "who
// is AS" gets answered. Only the reader's own may be taken back; the rest are
// there to be read.
type boostMenu struct {
	ctx     *Context
	comment reply
	boosts  []boost

	// adding is the field a new reaction is typed into, and typing says the
	// reader is in it rather than on the list.
	adding textinput.Model
	typing bool

	// taking is the reaction the reader has asked to take back and not yet
	// confirmed. Taking something away is never one key press — that is how a
	// boost gets removed by somebody who meant to look at it.
	taking *boost

	cursor int
	notice string
	saving bool
	wide   int
}

func newBoostMenu(ctx *Context, msg boostMenuMsg) *boostMenu {
	adding := textinput.New()
	adding.Prompt = ""
	adding.Placeholder = "🎉"
	adding.CharLimit = 16

	menu := &boostMenu{ctx: ctx, comment: msg.comment, boosts: msg.boosts, adding: adding}
	// An empty menu is not worth showing: nothing has been left on this comment,
	// so the only thing to do here is leave the first one.
	if len(msg.boosts) == 0 {
		menu.typing = true
	}
	return menu
}

func (b *boostMenu) Init() tea.Cmd {
	if b.typing {
		return b.adding.Focus()
	}
	return nil
}

func (b *boostMenu) Title() string {
	switch {
	case b.taking != nil:
		return "Take back " + b.taking.content + "?"
	case b.typing:
		return "Boost " + b.comment.author.firstName() + "'s comment"
	default:
		return "Boosts on " + b.comment.author.firstName() + "'s comment"
	}
}

// CapturingInput is true while a reaction is being typed, so the frame sends
// every key here instead of reading them as shortcuts.
func (b *boostMenu) CapturingInput() bool { return b.typing }

func (b *boostMenu) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if b.saving {
		return nil, true
	}
	if b.typing {
		return b.handleTypingKey(msg)
	}
	if b.taking != nil {
		return b.handleTakingKey(msg)
	}

	switch {
	case msg.Key().Code == tea.KeyEsc:
		return nil, false
	case msg.Key().Code == tea.KeyUp:
		b.cursor = max(b.cursor-1, 0)
	case msg.Key().Code == tea.KeyDown:
		b.cursor = min(b.cursor+1, max(len(b.boosts)-1, 0))
	case msg.String() == "a":
		b.typing, b.notice = true, ""
		return b.adding.Focus(), true
	case msg.String() == "i":
		return b.openCard()
	case msg.Key().Code == tea.KeyEnter, msg.String() == "x":
		return b.ask()
	}
	return nil, true
}

// ask puts a confirmation in front of taking a reaction back. Nothing here
// removes anything on one key press: enter over a list is how a reader looks at
// something, and it took a boost of mine away the first time somebody tried it.
func (b *boostMenu) ask() (tea.Cmd, bool) {
	chosen, ok := b.selected()
	if !ok {
		return nil, true
	}
	if !chosen.mine {
		// Saying whose it is beats a key that silently does nothing.
		b.notice = chosen.by.firstName() + " left that one, so it is not yours to take back."
		return nil, true
	}
	b.taking, b.notice = &chosen, ""
	return nil, true
}

func (b *boostMenu) handleTakingKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.Key().Code {
	case tea.KeyEsc:
		b.taking = nil
		return nil, true
	case tea.KeyEnter:
		taking := *b.taking
		b.taking, b.saving = nil, true
		return b.takeBack(taking), true
	}
	return nil, true
}

// openCard says who left the reaction the reader is standing on. A row of
// initials is all the comment itself has room for, and "who is AS" is a fair
// question to be able to ask.
func (b *boostMenu) openCard() (tea.Cmd, bool) {
	chosen, ok := b.selected()
	if !ok || !chosen.by.known() {
		return nil, true
	}
	who := chosen.by
	return func() tea.Msg { return personCardMsg{who: who} }, false
}

func (b *boostMenu) selected() (boost, bool) {
	if b.cursor < 0 || b.cursor >= len(b.boosts) {
		return boost{}, false
	}
	return b.boosts[b.cursor], true
}

func (b *boostMenu) handleTypingKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.Key().Code {
	case tea.KeyEsc:
		// Back to the list when there is one to go back to, and shut otherwise.
		if len(b.boosts) == 0 {
			return nil, false
		}
		b.typing = false
		b.adding.Blur()
		return nil, true
	case tea.KeyEnter:
		content := strings.TrimSpace(b.adding.Value())
		if content == "" {
			// The placeholder is what the web offers by default, so enter on an
			// empty field means that rather than nothing.
			content = b.adding.Placeholder
		}
		b.saving = true
		return b.leave(content), true
	}

	field, cmd := b.adding.Update(msg)
	b.adding = field
	return cmd, true
}

// --- Doing it ---

func (b *boostMenu) leave(content string) tea.Cmd {
	app, id := b.ctx.app, b.comment.id
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return commentChangedMsg{err: err}
		}
		if _, err := app.Account().Boosts().CreateRecording(b.ctx.Ctx(), id, content); err != nil {
			return commentChangedMsg{err: err}
		}
		return commentChangedMsg{said: "Boosted " + content}
	}
}

func (b *boostMenu) takeBack(left boost) tea.Cmd {
	app := b.ctx.app
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return commentChangedMsg{err: err}
		}
		if err := app.Account().Boosts().Delete(b.ctx.Ctx(), left.id); err != nil {
			return commentChangedMsg{err: err}
		}
		return commentChangedMsg{said: "Took back " + left.content}
	}
}

func (b *boostMenu) Update(msg tea.Msg) (tea.Cmd, bool) {
	changed, ok := msg.(commentChangedMsg)
	if !ok {
		if b.typing {
			field, cmd := b.adding.Update(msg)
			b.adding = field
			return cmd, false
		}
		return nil, false
	}

	b.saving = false
	if changed.err != nil {
		b.notice = errorNotice("That did not work", changed.err)
		return nil, true
	}
	return nil, false
}

// --- Rendering ---

func (b *boostMenu) View() string {
	styles := b.ctx.Styles()

	var rows []string
	if b.notice != "" {
		rows = append(rows, wrapText(b.notice, b.width())...)
		rows = append(rows, "")
	}

	switch {
	case b.taking != nil:
		rows = append(rows, wrapText("Take back "+b.taking.content+"? It goes from "+
			b.comment.author.firstName()+"'s comment for everybody.", b.width())...)
		rows = append(rows, "", styles.Muted.Render("enter to take it back · esc to keep it"))
	case b.typing:
		rows = append(rows, styles.Muted.Render("Leave a boost — a word or an emoji."), "", b.adding.View())
	default:
		rows = append(rows, b.list()...)
	}

	if b.saving {
		rows = append(rows, "", styles.Muted.Render("Saving…"))
	}
	return strings.Join(rows, "\n")
}

// list is every reaction: what was left, then a quiet dot, then who left it.
func (b *boostMenu) list() []string {
	styles := b.ctx.Styles()

	rows := make([]string, 0, len(b.boosts))
	for index, left := range b.boosts {
		trailing := ""
		if left.mine {
			trailing = "yours"
		}
		rows = append(rows, itemRow{
			label:    left.content + styles.Muted.Render(" · ") + left.by.name,
			trailing: trailing,
			selected: index == b.cursor,
		}.render(styles, b.width()))
	}
	return rows
}

func (b *boostMenu) HelpBindings() []helpBinding {
	switch {
	case b.taking != nil:
		return []helpBinding{{"enter", "take it back"}, {"esc", "keep it"}}
	case b.typing:
		return []helpBinding{{"enter", "boost"}, {"esc", "back"}}
	default:
		return []helpBinding{
			{"↑↓", "move"}, {"a", "add a boost"}, {"i", "about them"},
			{"x", "take back"}, {"esc", "close"},
		}
	}
}

func (b *boostMenu) Resize(width, height int) {
	b.adding.SetWidth(max(width, 1))
	b.wide = width
}

func (b *boostMenu) width() int { return max(b.wide, 1) }
