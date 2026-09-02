package workspace

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// commentActionsMsg asks the model to open the actions over one comment.
type commentActionsMsg struct {
	comment reply
	mine    bool
}

// commentChangedMsg says a comment was edited, trashed or boosted, so the screen
// under the modal reads its comments again. The server decides what is there
// now; patching the list from here would show what was asked for rather than
// what happened.
type commentChangedMsg struct {
	said string
	err  error
}

// What the modal is showing. It opens on the list of actions, and the one that
// needs something typed and the one that needs confirming take it over rather
// than opening a modal of their own — a form stacked on a form leaves the reader
// no way to tell what esc closes.
type commentMode int

const (
	commentModeActions commentMode = iota
	commentModeEdit
	commentModeTrash
)

// commentActions is what the reader can do to one comment.
//
// Which actions depends on whose comment it is: anyone may bookmark one or copy
// a link to it, and only its author may edit or trash it. The server enforces
// that too — this is about not offering what will be refused.
//
// Boosting is not here. It is its own menu behind its own key, because it is the
// one thing on a comment a reader does over and over, and burying "🎉" two key
// presses deep makes it the rarest.
type commentActions struct {
	ctx     *Context
	comment reply
	mine    bool

	mode   commentMode
	cursor int
	notice string
	saving bool

	// body is what an edit is typed into.
	body composer

	// wide is the room inside the frame, which the model hands over on a resize.
	wide int
}

func newCommentActions(ctx *Context, msg commentActionsMsg) *commentActions {
	body := newComposer("")
	body.SetValue(msg.comment.body)

	return &commentActions{ctx: ctx, comment: msg.comment, mine: msg.mine, body: body}
}

func (c *commentActions) Init() tea.Cmd { return nil }

func (c *commentActions) Title() string {
	switch c.mode {
	case commentModeEdit:
		return "Edit comment"
	case commentModeTrash:
		return "Move to trash?"
	default:
		return c.comment.author.firstName() + "'s comment"
	}
}

// --- What there is to do ---

// action is one row of the menu.
type action struct {
	label string
	mode  commentMode
	run   func(*commentActions) tea.Cmd
}

func (c *commentActions) actions() []action {
	actions := []action{
		{label: "Copy link", run: (*commentActions).copyLink},
		{label: "Bookmark", run: (*commentActions).bookmark},
	}
	if c.mine {
		actions = append(actions,
			action{label: "Edit", mode: commentModeEdit},
			action{label: "Move to trash", mode: commentModeTrash},
		)
	}
	return actions
}

// --- Keys ---

func (c *commentActions) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if c.saving {
		return nil, true
	}

	switch c.mode {
	case commentModeEdit:
		return c.handleEditKey(msg)
	case commentModeTrash:
		return c.handleTrashKey(msg)
	default:
		return c.handleActionsKey(msg)
	}
}

func (c *commentActions) handleActionsKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	actions := c.actions()
	switch msg.Key().Code {
	case tea.KeyEsc:
		return nil, false
	case tea.KeyUp:
		c.cursor = max(c.cursor-1, 0)
	case tea.KeyDown:
		c.cursor = min(c.cursor+1, len(actions)-1)
	case tea.KeyEnter:
		chosen := actions[c.cursor]
		if chosen.run != nil {
			return chosen.run(c), false
		}
		c.mode = chosen.mode
		c.notice = ""
		if c.mode == commentModeEdit {
			return c.body.Focus(), true
		}
		return nil, true
	}
	return nil, true
}

func (c *commentActions) handleEditKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case msg.Key().Code == tea.KeyEsc:
		return nil, false
	case msg.Key().Code == tea.KeyEnter && msg.Key().Mod == 0:
		said := strings.TrimSpace(c.body.Value())
		if said == "" {
			c.notice = "A comment cannot be empty."
			return nil, true
		}
		c.saving = true
		return c.saveEdit(said), true
	}

	return c.body.edit(msg), true
}

func (c *commentActions) handleTrashKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.Key().Code {
	case tea.KeyEsc:
		return nil, false
	case tea.KeyEnter:
		c.saving = true
		return c.trash(), true
	}
	return nil, true
}

// CapturingInput is true while the edit is being typed, so the frame sends every
// key here rather than reading them as shortcuts.
func (c *commentActions) CapturingInput() bool { return c.mode == commentModeEdit }

// --- Doing it ---

func (c *commentActions) copyLink() tea.Cmd {
	if c.comment.url == "" {
		return notifyError("There is no link to this comment", nil)
	}
	return tea.Batch(tea.SetClipboard(c.comment.url),
		notify("Copied the link to "+c.comment.author.firstName()+"'s comment"))
}

func (c *commentActions) bookmark() tea.Cmd {
	app, id := c.ctx.app, c.comment.id
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return commentChangedMsg{err: err}
		}
		if _, err := app.Account().Bookmarks().Create(c.ctx.Ctx(), id); err != nil {
			return commentChangedMsg{err: err}
		}
		return commentChangedMsg{said: "Bookmarked"}
	}
}

func (c *commentActions) saveEdit(said string) tea.Cmd {
	app, id := c.ctx.app, c.comment.id
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return commentChangedMsg{err: err}
		}
		req := &basecamp.UpdateCommentRequest{Content: richtext.MarkdownToHTML(said)}
		if _, err := app.Account().Comments().Update(c.ctx.Ctx(), id, req); err != nil {
			return commentChangedMsg{err: err}
		}
		return commentChangedMsg{said: "Saved"}
	}
}

func (c *commentActions) trash() tea.Cmd {
	app, id := c.ctx.app, c.comment.id
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return commentChangedMsg{err: err}
		}
		if err := app.Account().Comments().Trash(c.ctx.Ctx(), id); err != nil {
			return commentChangedMsg{err: err}
		}
		return commentChangedMsg{said: "Moved to trash"}
	}
}

// Update takes the answer to its own write. A failure keeps the modal open with
// what the reader typed still in it; a success is the model's to close.
func (c *commentActions) Update(msg tea.Msg) (tea.Cmd, bool) {
	changed, ok := msg.(commentChangedMsg)
	if !ok {
		if c.mode == commentModeEdit {
			return c.body.edit(msg), false
		}
		return nil, false
	}

	c.saving = false
	if changed.err != nil {
		c.notice = errorNotice("That did not work", changed.err)
		return nil, true
	}
	return nil, false
}

// --- Rendering ---

func (c *commentActions) View() string {
	styles := c.ctx.Styles()

	var rows []string
	if c.notice != "" {
		rows = append(rows, wrapText(c.notice, c.width())...)
		rows = append(rows, "")
	}

	switch c.mode {
	case commentModeEdit:
		rows = append(rows, c.body.rows()...)
	case commentModeTrash:
		rows = append(rows,
			wrapText(c.comment.author.firstName()+"'s comment goes to the trash. It can be brought back from there.", c.width())...)
		rows = append(rows, "", styles.Muted.Render("enter to move it · esc to keep it"))
	default:
		rows = append(rows, c.actionsView()...)
	}

	if c.saving {
		rows = append(rows, "", styles.Muted.Render("Saving…"))
	}
	return strings.Join(rows, "\n")
}

func (c *commentActions) actionsView() []string {
	styles := c.ctx.Styles()
	rows := make([]string, 0, len(c.actions()))
	for index, each := range c.actions() {
		rows = append(rows, itemRow{label: each.label, selected: index == c.cursor}.render(styles, c.width()))
	}
	return rows
}

func (c *commentActions) HelpBindings() []helpBinding {
	switch c.mode {
	case commentModeEdit:
		return []helpBinding{{"enter", "save"}, {"esc", "cancel"}}
	case commentModeTrash:
		return []helpBinding{{"enter", "move to trash"}, {"esc", "cancel"}}
	default:
		return []helpBinding{{"↑↓", "move"}, {"enter", "choose"}, {"esc", "close"}}
	}
}

func (c *commentActions) Resize(width, height int) {
	c.body.SetWidth(max(width, 1))
	c.body.SetHeight(max(min(height-2, 8), 3))
	c.wide = width
}

func (c *commentActions) width() int { return max(c.wide, 1) }

func firstName(who string) string {
	if first, _, found := strings.Cut(who, " "); found {
		return first
	}
	if who == "" {
		return "this"
	}
	return who
}
