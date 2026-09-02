package workspace

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

const (
	// The key that opens the form from the board it posts to.
	newMessageKey = "a"

	// The two ways of saving, from anywhere in the form: a reader who has just
	// typed the last word of the body should not have to walk back to a button.
	postMessageKey  = "ctrl+s"
	draftMessageKey = "ctrl+d"
)

// What a message is saved as. The API's own two words: a draft is only visible to
// whoever wrote it, and posting notifies the project.
const (
	messagePosted  = "active"
	messageDrafted = "drafted"
)

// bodyRows is how tall the body field is. A message board post is the long form
// of writing in Basecamp, so the body takes what the rest of the form leaves and
// this is only the floor on a short terminal.
const bodyRows = 8

// newMessageMsg asks the model for the form that writes a new message.
type newMessageMsg struct {
	board  tool
	bucket int64
}

// editMessageMsg asks the model for the same form over a message already
// written: the reader's own post, or a draft they are still working on.
type editMessageMsg struct{ message message }

// messageCategoriesMsg is the board's categories, which are a project's rather
// than a board's — one list of them serves every board in the project.
type messageCategoriesMsg struct {
	categories []messageCategory
	err        error
}

// messageWrittenMsg is the write coming back, with the message as the server has
// it now. draft says it is still a draft; wasPosted says it was already live
// before this, which is the difference between posting a message and saving a
// change to one.
type messageWrittenMsg struct {
	saved     message
	draft     bool
	wasPosted bool
	err       error
}

// messageSavedMsg says the write landed, so the form closes and whatever is
// under it reads again. saved is the message as the server has it now, which the
// screen underneath takes if it is the one showing it.
type messageSavedMsg struct {
	said  string
	saved message
}

func messageSaved(said string, saved message) tea.Cmd {
	return func() tea.Msg { return messageSavedMsg{said: said, saved: saved} }
}

// messageCategory is one of the labels a project offers its messages.
type messageCategory struct {
	id   int64
	name string
	icon string
}

func (c messageCategory) label() string {
	return strings.TrimSpace(c.icon + " " + c.name)
}

// The parts of the form, in the order tab walks them.
type messageField int

const (
	messageFieldCategory messageField = iota
	messageFieldSubject
	messageFieldBody
)

// messageForm writes a message on a board, whether that message exists yet or
// not: a new one, a draft being finished, or a post being corrected. They are the
// same three fields and the same two ways of saving, so they are the same form.
//
// It is a screen rather than a modal because it is what the reader is doing, not
// something they are doing over the board — and because a body worth typing wants
// the whole terminal.
type messageForm struct {
	ctx    *Context
	board  tool
	bucket int64

	// editing is the message being changed, and zero for a new one. posted says
	// it is already live, which is what decides whether saving it as a draft is
	// on offer: a message people have read does not go back to being unwritten.
	editing int64
	posted  bool

	subject textinput.Model
	body    composer

	// categories are the project's, read when the form opens. The row is skipped
	// entirely when a project has none, rather than offering a picker with
	// nothing in it.
	categories []messageCategory
	chosen     int

	// What the message held when the form opened, so esc can tell a change from
	// an untouched form. All three are empty for a new message, which makes an
	// untouched new form and an unchanged edit the same question.
	wasSubject  string
	wasBody     string
	wasCategory int64

	on      messageField
	saving  bool
	leaving bool
	notice  string

	width int
}

func newMessageForm(ctx *Context, msg newMessageMsg) *messageForm {
	return &messageForm{
		ctx:     ctx,
		board:   msg.board,
		bucket:  msg.bucket,
		subject: newSubjectField(),
		body:    newComposer("Write away… Markdown works here"),
		on:      messageFieldSubject,
	}
}

// newMessageEdit is the form over a message already written. What it holds is
// what the message holds, so the reader changes it rather than retyping it.
func newMessageEdit(ctx *Context, post message) *messageForm {
	subject := newSubjectField()
	subject.SetValue(post.subject)

	body := newComposer("Write away… Markdown works here")
	body.SetValue(post.body)

	return &messageForm{
		ctx:         ctx,
		bucket:      post.bucket,
		editing:     post.id,
		posted:      !post.draft,
		subject:     subject,
		body:        body,
		wasSubject:  post.subject,
		wasBody:     post.body,
		wasCategory: post.categoryID,
		on:          messageFieldSubject,
	}
}

func newSubjectField() textinput.Model {
	subject := textinput.New()
	subject.Prompt = ""
	subject.Placeholder = "Type a title…"
	return subject
}

func (f *messageForm) Init() tea.Cmd {
	return tea.Batch(
		f.subject.Focus(),
		loadMessageCategories(f.ctx.Ctx(), f.ctx.app, f.bucket),
	)
}

func (f *messageForm) Title() string {
	switch {
	case f.posted:
		return "Edit message"
	case f.editing != 0:
		return "Continue writing"
	default:
		return "New message"
	}
}

func (f *messageForm) Loading() bool { return false }

// CapturingInput is always true, even on the category row: a form is somewhere a
// reader types, and a digit typed into it is a digit rather than a jump to a
// section. Esc is handled here for the same reason.
func (f *messageForm) CapturingInput() bool { return true }

// WantsFullWidth keeps the notifications off this screen. Writing a message is
// what the reader is doing, not something they are doing beside anything else —
// and a body worth typing wants the columns.
func (f *messageForm) WantsFullWidth() bool { return true }

// --- Keys ---

func (f *messageForm) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if f.saving {
		return nil
	}
	if f.leaving {
		return f.handleLeavingKey(msg)
	}

	switch {
	case msg.String() == postMessageKey:
		return f.save(messagePosted)
	case msg.String() == draftMessageKey && !f.posted:
		return f.save(messageDrafted)
	}

	switch {
	case msg.Key().Code == tea.KeyEsc:
		return f.leave()
	case msg.Key().Code == tea.KeyTab && msg.Key().Mod&tea.ModShift != 0:
		return f.move(-1)
	case msg.Key().Code == tea.KeyTab:
		return f.move(1)
	}

	switch f.on {
	case messageFieldCategory:
		return f.handleCategoryKey(msg)
	case messageFieldSubject:
		// Enter in a one-line field means "done with this line", not a newline.
		if msg.Key().Code == tea.KeyEnter {
			return f.move(1)
		}
		field, cmd := f.subject.Update(msg)
		f.subject = field
		return cmd
	default:
		return f.body.edit(msg)
	}
}

// move walks the fields, skipping the category row when a project has none.
func (f *messageForm) move(by int) tea.Cmd {
	f.subject.Blur()
	f.body.Blur()

	next := messageField(max(min(int(f.on)+by, int(messageFieldBody)), int(messageFieldCategory)))
	if next == messageFieldCategory && len(f.categories) == 0 {
		next = messageFieldSubject
	}
	f.on = next

	switch f.on {
	case messageFieldSubject:
		return f.subject.Focus()
	case messageFieldBody:
		return f.body.Focus()
	default:
		return nil
	}
}

// handleCategoryKey walks the categories one at a time, with nothing chosen as
// the first stop: a category is optional, and "no category" has to be reachable
// again after picking one.
func (f *messageForm) handleCategoryKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyLeft, tea.KeyUp:
		if f.chosen == f.categoryFloor() && f.chosen > 0 {
			f.notice = "Taking a category off a message isn't possible through the API yet. You can swap it for another one."
			return nil
		}
		f.chosen = max(f.chosen-1, f.categoryFloor())
	case tea.KeyRight, tea.KeyDown:
		f.chosen = min(f.chosen+1, len(f.categories))
	}
	return nil
}

// categoryFloor is the leftmost stop on the picker: "no category" for a message
// being written, and the first real one when editing a message that already has
// one.
//
// A zero category id never reaches the wire — omitempty drops the key — and the
// server reads a missing category_id on an update as "leave it alone" rather than
// "clear it". So a category can be swapped but not removed, and the picker stops
// where the API does rather than offering a change that would be dropped. See
// "[SDK] UpdateMessageRequest can't clear a category" on the CLIs board.
func (f *messageForm) categoryFloor() int {
	if f.editing != 0 && f.wasCategory != 0 {
		return 1
	}
	return 0
}

// --- Leaving ---

// leave closes the form. Whatever was typed into it would go too, and writing is
// worth more than the key press it takes to confirm — so a form nobody changed
// closes and a changed one asks first.
func (f *messageForm) leave() tea.Cmd {
	if f.changed() {
		f.leaving = true
		f.notice = ""
		return nil
	}
	return closeScreen()
}

func (f *messageForm) handleLeavingKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyEnter:
		return closeScreen()
	case tea.KeyEsc:
		f.leaving = false
	}
	return nil
}

func (f *messageForm) changed() bool {
	if strings.TrimSpace(f.subject.Value()) != f.wasSubject ||
		strings.TrimSpace(f.body.Value()) != f.wasBody {
		return true
	}
	// Only once the list has landed: until then nothing is chosen because there
	// is nothing to choose from, which is not the reader clearing a category.
	return len(f.categories) > 0 && f.categoryID() != f.wasCategory
}

// --- Saving ---

// save writes what is in the form: a new message, or the one being edited. A
// title is the one thing the API insists on, so it is the one thing checked here
// rather than sent to be refused.
func (f *messageForm) save(status string) tea.Cmd {
	subject := strings.TrimSpace(f.subject.Value())
	if subject == "" {
		f.notice = "A message needs a title."
		f.on = messageFieldSubject
		f.body.Blur()
		return f.subject.Focus()
	}

	f.saving, f.notice = true, ""
	body := richtext.MarkdownToHTML(strings.TrimSpace(f.body.Value()))
	draft := status == messageDrafted

	if f.editing != 0 {
		return changeMessage(f.ctx.Ctx(), f.ctx.app, f.editing, &basecamp.UpdateMessageRequest{
			Subject:    subject,
			Content:    body,
			Status:     status,
			CategoryID: f.categoryID(),
		}, draft, f.posted)
	}
	return postMessage(f.ctx.Ctx(), f.ctx.app, f.board.id, &basecamp.CreateMessageRequest{
		Subject:    subject,
		Content:    body,
		Status:     status,
		CategoryID: f.categoryID(),
	}, draft)
}

// categoryID is the chosen category, and zero for none — which is what the API
// reads as "no category" and what omitempty leaves off the wire.
func (f *messageForm) categoryID() int64 {
	if f.chosen <= 0 || f.chosen > len(f.categories) {
		return 0
	}
	return f.categories[f.chosen-1].id
}

// selectCategory stands the picker on the category a message already has. A
// category the project no longer offers leaves the picker on none, which is what
// saving it would do anyway.
func (f *messageForm) selectCategory(id int64) {
	for index, each := range f.categories {
		if each.id == id {
			f.chosen = index + 1
			return
		}
	}
}

func (f *messageForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case messageCategoriesMsg:
		// A project with no categories, or one whose categories could not be read,
		// gets a form without that row rather than an empty picker.
		if msg.err == nil {
			f.categories = msg.categories
			f.selectCategory(f.wasCategory)
		}
		return nil, true

	case messageWrittenMsg:
		f.saving = false
		if msg.err != nil {
			f.notice = errorNotice("Could not save the message", msg.err)
			return nil, true
		}
		// A draft nobody else can see is easy to mistake for a post, and a change
		// to something already read is not a posting, so each of the three says
		// what it was.
		said := "Posted " + msg.saved.subject
		switch {
		case msg.draft:
			said = "Saved " + msg.saved.subject + " as a draft"
		case msg.wasPosted:
			said = "Saved " + msg.saved.subject
		}
		return messageSaved(said, msg.saved), true
	}

	// A cursor blink belongs to whichever field has the keys. The category row has
	// no field to blink.
	switch f.on {
	case messageFieldSubject:
		field, cmd := f.subject.Update(msg)
		f.subject = field
		return cmd, false
	case messageFieldBody:
		return f.body.edit(msg), false
	case messageFieldCategory:
		return nil, false
	}
	return nil, false
}

// --- Rendering ---

func (f *messageForm) View() string {
	styles := f.ctx.Styles()

	if f.leaving {
		return strings.Join(append(
			wrapText("This message has not been saved. Leaving loses it.", f.room()),
			"",
			styles.Muted.Render("enter to leave it · esc to keep writing"),
		), "\n")
	}

	var rows []string
	if f.notice != "" {
		rows = append(rows, wrapText(f.notice, f.room())...)
		rows = append(rows, "")
	}

	if len(f.categories) > 0 {
		rows = append(rows, f.label("Category", messageFieldCategory), f.categoryRow(), "")
	}

	rows = append(rows, f.label("Title", messageFieldSubject), f.subject.View(), "")
	rows = append(rows, f.label("Message", messageFieldBody))
	rows = append(rows, f.body.rows()...)

	if f.saving {
		rows = append(rows, "", styles.Muted.Render("Saving…"))
	}
	return strings.Join(rows, "\n")
}

// categoryRow shows the one category that is chosen, with chevrons for the ones
// either side of it. One at a time rather than all of them along a line: a
// project can have a dozen, and a row of a dozen is wider than the form.
func (f *messageForm) categoryRow() string {
	styles := f.ctx.Styles()

	label := "No category"
	if f.chosen > 0 && f.chosen <= len(f.categories) {
		label = f.categories[f.chosen-1].label()
	}

	chosen := styles.Muted
	if f.on == messageFieldCategory {
		chosen = lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true)
	}

	return strings.Join([]string{
		f.chevron("‹", f.chosen > f.categoryFloor()),
		chosen.Render(label),
		f.chevron("›", f.chosen < len(f.categories)),
	}, " ")
}

// chevron says whether there is another category that way, and holds its column
// when there isn't so the label stays put as the reader walks the list.
func (f *messageForm) chevron(arrow string, more bool) string {
	if !more {
		return " "
	}
	return f.ctx.Styles().Muted.Render(arrow)
}

// label names a field, and says which one the keys are going to.
func (f *messageForm) label(name string, field messageField) string {
	styles := f.ctx.Styles()
	if f.on == field {
		return lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true).Render(name)
	}
	return styles.Muted.Render(name)
}

func (f *messageForm) HelpBindings() []helpBinding {
	if f.leaving {
		return []helpBinding{{"enter", "leave"}, {"esc", "keep writing"}}
	}
	bindings := []helpBinding{{"tab", "next field"}}
	if f.on == messageFieldCategory {
		bindings = append(bindings, helpBinding{"←→", "category"})
	}

	// A message already posted has one way to save. One still unwritten has two:
	// post it, or keep it to yourself a while longer.
	if f.posted {
		return append(bindings, helpBinding{postMessageKey, "save"}, helpBinding{"esc", "cancel"})
	}
	return append(bindings,
		helpBinding{postMessageKey, "post"},
		helpBinding{draftMessageKey, "save draft"},
		helpBinding{"esc", "cancel"},
	)
}

func (f *messageForm) Resize(width, height int) {
	f.width = width
	f.subject.SetWidth(f.room())
	f.body.SetWidth(f.room())
	f.body.SetHeight(max(height-f.chromeRows(), bodyRows))
}

// chromeRows is everything the body is not: the labels, the fields above it, and
// the blank lines between them.
func (f *messageForm) chromeRows() int {
	rows := 6
	if len(f.categories) > 0 {
		rows += 3
	}
	return rows
}

func (f *messageForm) room() int { return max(f.width, 1) }

// --- Reading and writing ---

func loadMessageCategories(ctx context.Context, app *appctx.App, bucket int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messageCategoriesMsg{err: err}
		}
		result, err := app.Account().MessageTypes().List(ctx, bucket, nil)
		if err != nil {
			return messageCategoriesMsg{err: err}
		}

		categories := make([]messageCategory, 0, len(result.MessageTypes))
		for _, each := range result.MessageTypes {
			categories = append(categories, messageCategory{id: each.ID, name: each.Name, icon: each.Icon})
		}
		return messageCategoriesMsg{categories: categories}
	}
}

func postMessage(ctx context.Context, app *appctx.App, boardID int64, req *basecamp.CreateMessageRequest, draft bool) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messageWrittenMsg{err: err}
		}
		post, err := app.Account().Messages().Create(ctx, boardID, req)
		if err != nil {
			return messageWrittenMsg{err: err}
		}
		return messageWrittenMsg{saved: toMessage(*post), draft: draft}
	}
}

func changeMessage(ctx context.Context, app *appctx.App, messageID int64, req *basecamp.UpdateMessageRequest, draft, wasPosted bool) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messageWrittenMsg{err: err}
		}
		post, err := app.Account().Messages().Update(ctx, messageID, req)
		if err != nil {
			return messageWrittenMsg{err: err}
		}
		return messageWrittenMsg{saved: toMessage(*post), draft: draft, wasPosted: wasPosted}
	}
}
