package workspace

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// The key that opens the reader's own message in the form it was written in.
const editMessageKey = "e"

// messageTab is one of the two the web puts above a message's comments.
type messageTab int

const (
	tabComments messageTab = iota
	tabReferences
)

// messageScreen is one post read in full: its subject, its body, and the comments
// under it.
//
// Nothing is read to draw the post itself. The board's own list carries the body
// — api/messages/_message.json.jbuilder renders the rich text into the index —
// so the subject and the words are already here when the screen opens, and the
// only read is for the comments.
type messageScreen struct {
	ctx *Context

	post message

	// words is the body ready to draw, and shown the pictures in it and beside
	// the names under it. Both are components a card holds too — a message is
	// not the only thing in Basecamp with rich text and comments under it.
	words   body
	shown   *pictures
	answers *commentList

	// me is the reader, which is what says the message is theirs to edit.
	me int64

	// tab is which of the two the web shows is open.
	tab messageTab

	offset int
	width  int
	height int

	now func() time.Time
}

func newMessage(ctx *Context, post message) *messageScreen {
	return &messageScreen{
		ctx:     ctx,
		post:    post,
		words:   newBodyFromMarkdown(post.body, post.images),
		shown:   newPictures(ctx),
		answers: newCommentList(ctx),
		now:     time.Now,
	}
}

func (m *messageScreen) Init() tea.Cmd {
	return tea.Batch(
		m.shown.read(m.words),
		m.shown.readFaces([]string{m.post.author.avatar}),
		m.answers.read(m.post.id, m.post.comments),
	)
}

func (m *messageScreen) Title() string { return m.post.subject }

// replace takes the message as the server has it after an edit. The body is
// split again and the pictures dropped: what the words say has changed, and so
// may which pictures they carry. Init is what reads them back.
func (m *messageScreen) replace(post message) {
	m.post = post
	m.words = newBodyFromMarkdown(post.body, post.images)
	m.shown.forget()
	m.answers.cursor = -1
	m.offset = 0
}

// mine says the message is the reader's own, which is what decides whether
// editing it is on offer. The server decides too — this is about not offering
// what will be refused.
func (m *messageScreen) mine() bool {
	return m.me != 0 && m.post.author.id == m.me
}

// Loading is false while the comments are in flight. The post is already on
// screen, and a spinner over the whole screen would take away what the reader
// opened it for; the comments section says it is waiting instead.
func (m *messageScreen) Loading() bool { return false }

func (m *messageScreen) Resize(width, height int) {
	m.width = width
	m.height = height
	m.shown.resize(width)
	m.clampOffset()
}

// Update hands what arrives to whichever component it belongs to. The comments
// answer first: their read is the one that names everybody, and the pictures
// beside those names are asked for the moment it lands.
func (m *messageScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	if cmd, took := m.answers.update(msg); took {
		m.me = m.answers.me
		m.clampOffset()
		return tea.Batch(cmd,
			m.shown.readFaces(m.answers.people()),
			m.shown.read(m.words, m.answers.carried())), true
	}
	if cmd, took := m.shown.update(msg, m.words, m.answers.carried()); took {
		m.clampOffset()
		return cmd, true
	}
	return nil, false
}

// HandleKey scrolls the message and walks its comments. Up and down scroll,
// because a message is mostly something to read; tab moves between the two tabs,
// and j and k step from one comment to the next, which is what makes one
// selected and its actions reachable.
func (m *messageScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	// The comments answer first, and only while they are the open tab: the other
	// one has none to walk.
	if m.tab == tabComments {
		if cmd, took := m.answers.handleKey(msg); took {
			m.scrollToSelected()
			return cmd
		}
	}

	switch {
	case msg.Key().Code == tea.KeyTab:
		m.tab, m.answers.cursor = 1-m.tab, -1
		m.clampOffset()
		return nil
	case msg.String() == personCardKey:
		return m.openCard()
	case msg.String() == editMessageKey:
		return m.edit()
	}

	switch msg.Key().Code {
	case tea.KeyUp:
		m.offset = max(m.offset-1, 0)
	case tea.KeyDown:
		m.offset++
	case tea.KeyPgUp:
		m.offset = max(m.offset-m.height, 0)
	case tea.KeyPgDown:
		m.offset += m.height
	case tea.KeyHome:
		m.offset = 0
	case tea.KeyEnd:
		m.offset = len(m.layout())
	default:
		return nil
	}

	m.clampOffset()
	return nil
}

// edit opens the message in the form it was written in. Only the reader's own,
// and only from the message itself: standing on a comment, enter is what offers
// what can be done to that.
func (m *messageScreen) edit() tea.Cmd {
	if m.answers.cursor >= 0 || !m.mine() {
		return nil
	}
	post := m.post
	return func() tea.Msg { return editMessageMsg{message: post} }
}

// openCard says who wrote what the reader is standing on: the selected comment's
// author, or the message's own when they are reading the message itself.
func (m *messageScreen) openCard() tea.Cmd {
	who := m.post.author
	if selected, standing := m.answers.selected(); standing {
		who = selected.author
	}
	if !who.known() {
		return nil
	}
	return func() tea.Msg { return personCardMsg{who: who} }
}

// scrollToSelected brings the selected comment into view.
func (m *messageScreen) scrollToSelected() {
	rows := m.layout()
	m.offset = scrollTo(markedSpan(rows), m.offset, m.height, len(rows))
}

func (m *messageScreen) clampOffset() {
	m.offset = max(min(m.offset, max(len(m.layout())-m.height, 0)), 0)
}

func (m *messageScreen) HelpBindings() []helpBinding {
	bindings := []helpBinding{{"↑↓", "scroll"}, {"tab", "switch tab"}}
	if m.answers.cursor < 0 && m.mine() {
		bindings = append(bindings, helpBinding{editMessageKey, "edit"})
	}
	if m.tab == tabComments && len(m.answers.comments) > 0 {
		bindings = append(bindings, helpBinding{"j/k", "comment"})
		if m.answers.cursor >= 0 {
			bindings = append(bindings, helpBinding{"b", "boosts"}, helpBinding{"enter", "actions"})
		}
	}
	return bindings
}

// --- Rendering ---

func (m *messageScreen) View() string {
	rows := m.layout()
	end := min(m.offset+m.height, len(rows))
	return strings.Join(rows[min(m.offset, end):end], "\n")
}

// layout draws the post and then its comments. The rows are plain lines: nothing
// here is selectable, so they carry no item.
func (m *messageScreen) layout() []string {
	styles := m.ctx.Styles()
	theme := styles.Theme()
	heading := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)

	var rows []string
	now := m.now()

	// The subject leads, the way it does on the web's own page, with who wrote it
	// under and their picture beside both. The breadcrumb already carries the
	// subject too, but a trail truncates and a heading does not.
	face := m.shown.face(m.post.author.avatar)
	inner := m.width
	if len(face) > 0 {
		inner = max(m.width-avatarCols-2, 1)
	}
	header := append(
		wrapText(heading.Render(truncateToWidth(m.post.subject, inner)), inner),
		styles.Muted.Render(truncateToWidth(byline(m.post.author.name, m.post.author.title, since(m.post.at, now), m.post.kind), inner)),
	)
	rows = append(rows, besideFace(face, header)...)
	rows = append(rows, "")

	if m.words.empty() {
		rows = append(rows, styles.Muted.Render("This message has no body."))
	}
	rows = append(rows, m.shown.rows(m.words, styles, m.width)...)

	rows = append(rows, "")
	rows = append(rows, m.tabRows()...)
	rows = append(rows, "")

	if m.tab == tabReferences {
		// The web lists what links to this message here. The API serves neither
		// the list nor a count — backlinks is an HTML-only route — so there is
		// nothing honest to draw. See "[API] Serve a recording's backlinks so
		// references can be listed" on the CLIs board.
		rows = append(rows, "", styles.Muted.Render("  References aren't available through the API yet."))
		return rows
	}

	return append(rows, m.answers.rows(styles, m.shown, m.width, now, true)...)
}

// tabRows is the two tabs the web shows over the comments, drawn as tabs: the
// open one boxed with its bottom left off so it runs into the content below, and
// the rule carrying on under the shut one.
//
// Three lines rather than one underlined word. A tab is a shape — a thing
// standing in front of a line — and underlining a word says "link" in a terminal
// rather than "tab".
func (m *messageScreen) tabRows() []string {
	styles := m.ctx.Styles()
	theme := styles.Theme()
	open := lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	shut := lipgloss.NewStyle().Foreground(theme.Muted)
	edge := lipgloss.NewStyle().Foreground(theme.Border)

	labels := []string{"Comments", "References"}
	var top, middle, bottom strings.Builder
	for index, label := range labels {
		selected := int(m.tab) == index
		name, box := shut, edge
		if selected {
			name = open
		}

		top.WriteString(box.Render("╭" + strings.Repeat("─", len(label)+2) + "╮"))
		middle.WriteString(box.Render("│ ") + name.Render(label) + box.Render(" │"))
		if selected {
			// The open tab has no floor, so it reads as part of what is under it.
			bottom.WriteString(box.Render("╯" + strings.Repeat(" ", len(label)+2) + "╰"))
		} else {
			bottom.WriteString(box.Render("┴" + strings.Repeat("─", len(label)+2) + "┴"))
		}
		if index < len(labels)-1 {
			top.WriteString(" ")
			middle.WriteString(" ")
			bottom.WriteString(edge.Render("─"))
		}
	}

	// The hint sits on the tabs' own row, past the last of them, where it reads
	// as a note about them rather than about the comments underneath. It is the
	// first thing dropped when the column is too narrow to hold both.
	hint := "  tab to switch"
	if tui.DisplayWidth(middle.String())+len(hint) <= m.width {
		middle.WriteString(styles.Muted.Render(hint))
	}

	// The rule runs on to the end of the column, which is what the tabs are
	// standing in front of.
	drawn := tui.DisplayWidth(bottom.String())
	if rest := m.width - drawn; rest > 0 {
		bottom.WriteString(edge.Render(strings.Repeat("─", rest)))
	}
	return []string{top.String(), middle.String(), bottom.String()}
}
