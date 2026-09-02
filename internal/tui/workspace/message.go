package workspace

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// How many replies a message shows. The API's comments index pages, but a
// message with more replies than this is a conversation that outgrew its
// message, and the web's own "load more" is a better answer than a walk that
// never ends.
const messageReplyLimit = 100

// The key that opens the reader's own message in the form it was written in.
const editMessageKey = "e"

// messageTab is one of the two the web puts above a message's replies.
type messageTab int

const (
	tabComments messageTab = iota
	tabReferences
)

// reply is one comment under a message.
type reply struct {
	id     int64
	author person
	body   string
	at     time.Time

	// url is where the comment lives on the web, which is what "copy link"
	// copies; mine says the reader wrote it, which is what decides whether
	// editing and trashing are offered.
	url  string
	mine bool

	// boosted is how many reactions the comment carries, which the comment
	// itself says. It is what decides whether the reactions are worth a request:
	// most comments have none, and the API only lists them one recording at a
	// time.
	boosted int
}

// messageRepliesMsg is the answer to a read of a message's comments. me is who
// the reader is, which comes along because it is what says which of the comments
// are theirs.
type messageRepliesMsg struct {
	me      int64
	replies []reply
	err     error
}

// readerMsg is who the reader is on its own, for a message with no comments to
// carry the answer.
type readerMsg struct{ me int64 }

// loadReader asks who the reader is. A failure is not worth saying anything
// about: nothing is marked as theirs, which is what happens when the answer is
// nobody.
func loadReader(ctx context.Context, app *appctx.App) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return readerMsg{}
		}
		who, err := app.Account().People().Me(ctx)
		if err != nil || who == nil {
			return readerMsg{}
		}
		return readerMsg{me: who.ID}
	}
}

// messageScreen is one post read in full: its subject, its body, and the replies
// under it.
//
// Nothing is read to draw the post itself. The board's own list carries the body
// — api/messages/_message.json.jbuilder renders the rich text into the index —
// so the subject and the words are already here when the screen opens, and the
// only read is for the replies.
type messageScreen struct {
	ctx *Context

	post message
	// parts is the body split into prose and pictures, settled once when the
	// screen opens: the split is a property of the body, not of the window.
	parts    []bodyPart
	pictures map[string]tui.RenderedImage
	images   tui.ImageRenderer
	budget   *imageBudget

	// queue is the pictures still to read, in the order they appear; arrived is
	// what has been read and is waiting for the next redraw; waiting says a wait
	// is already armed, so a run of arrivals arms one rather than one each.
	queue   []string
	arrived map[string][]byte
	waiting bool

	// spin is the throbber's frame, and spinning says one tick is already in
	// flight so a run of arrivals does not arm one each.
	spin     int
	spinning bool

	// faces are the people's pictures, by the address each was read from. One per
	// person rather than one per comment: a thread is mostly the same few people.
	//
	// They get a budget of their own rather than sharing the body's. Two reasons,
	// and both bit: the two reads are batched, so one budget would be counted
	// down from two goroutines at once; and a message carrying twenty
	// screenshots would spend every slot on them and leave nothing for the
	// faces, which is exactly what it did.
	faces      map[string]tui.RenderedImage
	faceBudget *imageBudget

	// facesComing is the pictures asked for and not yet on screen, which is what
	// the throbber in their place stands for.
	facesComing map[string]struct{}

	// tab is which of the two the web shows is open; cursor is the comment the
	// reader is standing on, and -1 when they are reading the message itself.
	tab    messageTab
	cursor int

	// boosts are the reactions under each comment, by comment. me is the reader,
	// which is what says whose comments and boosts are theirs to change.
	boosts map[int64][]boost
	me     int64

	replies []reply
	// reading is true until the replies land, and notice is what to say when they
	// did not. The post is already on screen either way, so a failed read costs
	// the replies and nothing else.
	reading bool
	notice  string

	offset int
	width  int
	height int

	now func() time.Time
}

func newMessage(ctx *Context, post message) *messageScreen {
	return &messageScreen{
		ctx:        ctx,
		post:       post,
		parts:      splitBody(post.body),
		pictures:   map[string]tui.RenderedImage{},
		faces:      map[string]tui.RenderedImage{},
		boosts:     map[int64][]boost{},
		cursor:     -1,
		images:     tui.NewImageRenderer(),
		budget:     newImageBudget(),
		faceBudget: newFaceBudget(),
		now:        time.Now,
	}
}

func (m *messageScreen) Init() tea.Cmd {
	cmds := []tea.Cmd{m.readImages(), m.readFaces()}
	if m.post.comments > 0 {
		m.reading = true
		cmds = append(cmds, loadMessageReplies(m.ctx.Ctx(), m.ctx.app, m.post.id))
	} else {
		// Who the reader is comes along with the replies. With no replies to read
		// it is still wanted: it is what says the message is theirs to edit.
		cmds = append(cmds, loadReader(m.ctx.Ctx(), m.ctx.app))
	}
	return tea.Batch(cmds...)
}

func (m *messageScreen) Title() string { return m.post.subject }

// replace takes the message as the server has it after an edit. The body is
// split again and the pictures dropped: what the words say has changed, and so
// may which pictures they carry. Init is what reads them back.
func (m *messageScreen) replace(post message) {
	m.post = post
	m.parts = splitBody(post.body)
	m.pictures = map[string]tui.RenderedImage{}
	m.budget = newImageBudget()
	m.offset, m.cursor = 0, -1
}

// mine says the message is the reader's own, which is what decides whether
// editing it is on offer. The server decides too — this is about not offering
// what will be refused.
func (m *messageScreen) mine() bool {
	return m.me != 0 && m.post.author.id == m.me
}

// Loading is false while the replies are in flight. The post is already on
// screen, and a spinner over the whole screen would take away what the reader
// opened it for; the replies section says it is waiting instead.
func (m *messageScreen) Loading() bool { return false }

func (m *messageScreen) Resize(width, height int) {
	m.width = width
	m.height = height
	m.clampOffset()
}

func (m *messageScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case readerMsg:
		m.me = msg.me
		return nil, true

	case messageRepliesMsg:
		m.reading = false
		if msg.err != nil {
			m.notice = errorNotice("Could not load the replies", msg.err)
			return nil, true
		}
		m.me, m.replies = msg.me, msg.replies
		m.clampOffset()
		// The repliers and what they were boosted with are only known now, so
		// both are asked for now.
		return tea.Batch(m.readFaces(), m.readBoosts()), true

	case boostsMsg:
		if msg.err == nil {
			m.boosts[msg.comment] = msg.boosts
			m.clampOffset()
		}
		return nil, true

	case avatarsMsg:
		m.facesArrived(msg.asked, msg.avatars)
		return m.drawFaces(msg.avatars), true

	case facesPlacedMsg:
		m.placeFaces(msg.drawn)
		m.clampOffset()
		return m.spinImages(), true

	case messageImageMsg:
		return m.imageArrived(msg), true

	case imagesDueMsg:
		return m.drawImages(), true

	case imageSpinMsg:
		return m.advanceImageSpinner(), true

	case messageImagesPlacedMsg:
		m.placeImages(msg.drawn)
		m.clampOffset()
		return nil, true
	}
	return nil, false
}

// HandleKey scrolls the message and walks its comments. Up and down scroll,
// because a message is mostly something to read; tab moves between the two tabs,
// and j and k step from one comment to the next, which is what makes one
// selected and its actions reachable.
func (m *messageScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case msg.Key().Code == tea.KeyTab:
		m.tab, m.cursor = 1-m.tab, -1
		m.clampOffset()
		return nil
	case msg.String() == "j":
		return m.step(1)
	case msg.String() == "k":
		return m.step(-1)
	case msg.String() == "b":
		return m.openBoosts()
	case msg.String() == "i":
		return m.openCard()
	case msg.String() == editMessageKey:
		return m.edit()
	case msg.Key().Code == tea.KeyEnter:
		return m.openActions()
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

// step moves the selection from one comment to the next, and off the end of them
// back to the message itself.
func (m *messageScreen) step(by int) tea.Cmd {
	if m.tab != tabComments || len(m.replies) == 0 {
		return nil
	}
	m.cursor = max(min(m.cursor+by, len(m.replies)-1), -1)
	m.scrollToSelected()
	return nil
}

// openActions offers what can be done to the selected comment. Nothing selected
// is not an error: the reader is on the message itself, which has no actions of
// its own here.
func (m *messageScreen) openActions() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.replies) {
		return nil
	}
	selected := m.replies[m.cursor]
	return func() tea.Msg {
		return commentActionsMsg{comment: selected, mine: selected.mine}
	}
}

// edit opens the message in the form it was written in. Only the reader's own,
// and only from the message itself: standing on a comment, enter is what offers
// what can be done to that.
func (m *messageScreen) edit() tea.Cmd {
	if m.cursor >= 0 || !m.mine() {
		return nil
	}
	post := m.post
	return func() tea.Msg { return editMessageMsg{message: post} }
}

// openBoosts shows every reaction on the selected comment, with whoever left
// each one named in full.
//
// Its own key rather than a row in the actions, because boosting is the one
// thing a reader does to a comment over and over, and two key presses deep makes
// the commonest thing the rarest.
func (m *messageScreen) openBoosts() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.replies) {
		return nil
	}
	selected := m.replies[m.cursor]
	boosts := m.boosts[selected.id]
	return func() tea.Msg {
		return boostMenuMsg{comment: selected, boosts: boosts}
	}
}

// openCard says who wrote what the reader is standing on: the selected comment's
// author, or the message's own when they are reading the message itself.
func (m *messageScreen) openCard() tea.Cmd {
	who := m.post.author
	if m.cursor >= 0 && m.cursor < len(m.replies) {
		who = m.replies[m.cursor].author
	}
	if !who.known() {
		return nil
	}
	return func() tea.Msg { return personCardMsg{who: who} }
}

// readBoosts asks for the reactions on every comment that says it has any.
//
// All of them at once rather than the selected one, because a reaction is part
// of reading a thread, not part of acting on it — waiting to select a comment to
// find out it was boosted is finding out too late. The API lists them one
// recording at a time, so this is one request per comment; the count on each
// comment is what keeps that to the comments that actually have some, which on
// most threads is a handful.
func (m *messageScreen) readBoosts() tea.Cmd {
	reads := make([]tea.Cmd, 0, len(m.replies))
	for _, answer := range m.replies {
		if answer.boosted == 0 {
			continue
		}
		if _, read := m.boosts[answer.id]; read {
			continue
		}
		reads = append(reads, loadBoosts(m.ctx.Ctx(), m.ctx.app, m.me, answer.id))
	}
	if len(reads) == 0 {
		return nil
	}
	return tea.Batch(reads...)
}

// scrollToSelected brings the selected comment into view.
func (m *messageScreen) scrollToSelected() {
	if m.height <= 0 || m.cursor < 0 {
		m.clampOffset()
		return
	}

	rows, at := m.layoutMarking(m.cursor)
	if at.first < 0 {
		m.clampOffset()
		return
	}
	m.offset = min(m.offset, at.first)
	if at.last >= m.offset+m.height {
		m.offset = at.last - m.height + 1
	}
	m.offset = max(min(m.offset, max(len(rows)-m.height, 0)), 0)
}

func (m *messageScreen) clampOffset() {
	m.offset = max(min(m.offset, max(len(m.layout())-m.height, 0)), 0)
}

func (m *messageScreen) HelpBindings() []helpBinding {
	bindings := []helpBinding{{"↑↓", "scroll"}, {"tab", "switch tab"}}
	if m.cursor < 0 && m.mine() {
		bindings = append(bindings, helpBinding{editMessageKey, "edit"})
	}
	if m.tab == tabComments && len(m.replies) > 0 {
		bindings = append(bindings, helpBinding{"j/k", "comment"})
		if m.cursor >= 0 {
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

// layout draws the post and then its replies. The rows are plain lines: nothing
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
	face := m.face(m.post.author.avatar)
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

	if len(m.parts) == 0 {
		rows = append(rows, styles.Muted.Render("This message has no body."))
	}
	for _, part := range m.parts {
		if !part.isImage() {
			rows = append(rows, renderBody(part.text, m.width)...)
			continue
		}
		if cells := m.picture(part); cells != nil {
			rows = append(rows, cells...)
			if part.alt != "" {
				rows = append(rows, styles.Muted.Render(truncateToWidth(part.alt, m.width)))
			}
			rows = append(rows, "")
			continue
		}
		if m.coming(part) {
			// A picture on its way says so where it will appear, so a reader
			// watching a message full of screenshots knows to wait rather than
			// taking the gap for the whole message.
			rows = append(rows, styles.Muted.Render(truncateToWidth(m.comingLabel(part), m.width)), "")
			continue
		}
		// No picture to draw — the terminal cannot draw one, or the read failed.
		// What the image was called and where it lives is what is left to say,
		// which is what renderBody makes of the image markup on its own.
		rows = append(rows, renderBody(part.markdown(), m.width)...)
	}

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

	switch {
	case m.notice != "":
		rows = append(rows, wrapText(m.notice, m.width)...)
	case m.reading:
		rows = append(rows, styles.Muted.Render("Loading…"))
	case len(m.replies) == 0:
		rows = append(rows, styles.Muted.Render("  No comments yet."))
	default:
		for index, answer := range m.replies {
			if index > 0 {
				// A rule between comments, the way the web separates its cards:
				// without one a two-line comment runs straight into the next
				// person's name.
				rows = append(rows, "", styles.Muted.Render(strings.Repeat("─", max(m.width, 1))), "")
			}
			rows = append(rows, m.commentRows(answer, now, index == m.cursor)...)
		}
	}
	return rows
}

// span is where a comment's rows start and end, which is what scrolling to one
// needs to know.
type span struct{ first, last int }

// layoutMarking draws the screen and reports where one comment's rows landed.
// The layout is the one source of what is on screen, so scrolling asks it rather
// than counting rows a second time and getting a different answer.
func (m *messageScreen) layoutMarking(comment int) ([]string, span) {
	rows := m.layout()
	at := span{first: -1, last: -1}
	if comment < 0 || comment >= len(m.replies) {
		return rows, at
	}

	// The selected comment is the one carrying the cursor's marker, which is the
	// only thing on screen that says which rows are its.
	for index, row := range rows {
		if strings.Contains(row, selectedMarker) {
			if at.first < 0 {
				at.first = index
			}
			at.last = index
		}
	}
	return rows, at
}

// tabRows is the two tabs the web shows over the replies, drawn as tabs: the
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

// selectedMarker stands in the left margin of the comment the reader is on. It
// is also how layoutMarking finds that comment's rows, so it appears nowhere
// else.
const selectedMarker = "▌"

// commentRows is one comment: who wrote it beside their picture, then what they
// said under both, then the boosts left on it.
func (m *messageScreen) commentRows(answer reply, now time.Time, selected bool) []string {
	styles := m.ctx.Styles()
	theme := styles.Theme()
	name := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)
	if selected {
		name = name.Foreground(theme.Primary)
	}

	face := m.face(answer.author.avatar)
	inner := max(m.width-2, 1)
	if len(face) > 0 {
		inner = max(inner-avatarCols-2, 1)
	}

	said := name.Render(truncateToWidth(answer.author.name, inner))
	if aside := byline("", answer.author.title, since(answer.at, now)); aside != "" {
		room := max(inner-tui.DisplayWidth(answer.author.name), 1)
		said += styles.Muted.Render(truncateToWidth(" · "+aside, room))
	}

	// The boosts go inside the block, not after it, so they line up under the
	// words they are reacting to rather than under the face.
	block := append([]string{said}, renderBody(answer.body, inner)...)
	if left := boostRows(styles, m.boosts[answer.id], inner); len(left) > 0 {
		block = append(block, "")
		block = append(block, left...)
	}
	inside := besideFace(face, block)

	// The margin says which comment is selected, down every row of it, the way a
	// quoted block is marked rather than a single row highlighted: a comment is
	// several lines and all of them are the comment.
	margin := "  "
	if selected {
		margin = lipgloss.NewStyle().Foreground(theme.Primary).Render(selectedMarker) + " "
	}
	rows := make([]string, 0, len(inside))
	for _, row := range inside {
		rows = append(rows, margin+row)
	}
	return rows
}

// byline joins what is said about a person beside their name — their title, when
// they wrote, the category — dropping whichever of them is missing.
func byline(who, title string, rest ...string) string {
	return strings.Join(nonEmpty(append([]string{who, title}, rest...)...), " · ")
}

// --- Reading ---

func loadMessageReplies(ctx context.Context, app *appctx.App, messageID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messageRepliesMsg{err: err}
		}

		result, err := app.Account().Comments().List(ctx, messageID, &basecamp.CommentListOptions{
			Limit: messageReplyLimit,
		})
		if err != nil {
			return messageRepliesMsg{err: err}
		}

		// Who the reader is decides which comments they may edit or trash. A
		// failure here is not the replies' failure: they are still worth showing,
		// with nothing marked as the reader's own.
		var me int64
		if who, err := app.Account().People().Me(ctx); err == nil && who != nil {
			me = who.ID
		}

		replies := make([]reply, 0, len(result.Comments))
		for _, comment := range result.Comments {
			replies = append(replies, toReply(comment, me))
		}
		return messageRepliesMsg{me: me, replies: replies}
	}
}

func toReply(comment basecamp.Comment, me int64) reply {
	out := reply{
		id:      comment.ID,
		body:    strings.TrimSpace(richtext.HTMLToMarkdown(comment.Content)),
		at:      comment.CreatedAt.Local(),
		url:     comment.AppURL,
		boosted: comment.BoostsCount,
	}
	if comment.Creator != nil {
		out.author = toPerson(comment.Creator)
		out.mine = me != 0 && comment.Creator.ID == me
	}
	return out
}
