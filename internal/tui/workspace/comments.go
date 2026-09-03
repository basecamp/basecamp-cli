package workspace

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// How many comments a recording asks for at once. Past this a thread is a thread
// nobody reads to the bottom of.
const commentPageLimit = 100

// comment is one thing somebody said under a recording.
type comment struct {
	id     int64
	author person
	at     time.Time

	// words is what they said, ready to draw — pictures and all. A comment
	// carries screenshots as often as a message does.
	words body

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

// commentsMsg is the answer to a read of a recording's comments. me is who the
// reader is, which comes along because it is what says which of the comments are
// theirs.
type commentsMsg struct {
	recording int64
	me        int64
	comments  []comment
	err       error
}

// readerMsg is who the reader is on its own, for a recording with no comments to
// carry the answer.
type readerMsg struct{ me int64 }

// commentList is the comments under one recording: the read, the cursor that
// walks them, the reactions on them, and the rows they draw as.
//
// A message has these under it, and so does a card, and so will everything else
// Basecamp lets people talk about — so this is a component the screens hold
// rather than a set of fields each of them keeps its own copy of.
type commentList struct {
	ctx *Context

	// recording is what the comments hang off, and what a read asks for.
	recording int64

	comments []comment

	// boosts are the reactions under each comment, by comment. me is the reader,
	// which is what says whose comments and reactions are theirs to change.
	boosts map[int64][]boost
	me     int64

	// cursor is the comment the reader is standing on, and -1 when they are
	// reading the recording itself rather than the answers to it.
	cursor int

	// reading is true until the comments land; notice is what to say when they
	// did not.
	reading bool
	notice  string
}

func newCommentList(ctx *Context) *commentList {
	return &commentList{ctx: ctx, boosts: map[int64][]boost{}, cursor: -1}
}

// read asks for the comments under a recording, or for who the reader is when
// the recording says it has none: that answer is wanted either way, because it
// is what says the recording is theirs to edit.
func (l *commentList) read(recording int64, expected int) tea.Cmd {
	l.recording = recording
	l.notice = ""
	if expected == 0 {
		l.comments = nil
		return loadReader(l.ctx.Ctx(), l.ctx.app)
	}
	l.reading = true
	return loadComments(l.ctx.Ctx(), l.ctx.app, recording)
}

// update takes the messages the reads answer with, and says whether it took one.
func (l *commentList) update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case readerMsg:
		l.me = msg.me
		return nil, true

	case commentsMsg:
		if msg.recording != l.recording {
			return nil, false
		}
		l.reading = false
		if msg.err != nil {
			l.notice = errorNotice("Could not load the comments", msg.err)
			return nil, true
		}
		l.me, l.comments = msg.me, msg.comments
		l.clampCursor()
		// Who answered and what they were boosted with are only known now, so
		// both are asked for now.
		return l.readBoosts(), true

	case boostsMsg:
		if msg.err == nil {
			l.boosts[msg.comment] = msg.boosts
		}
		return nil, true
	}
	return nil, false
}

// readBoosts asks for the reactions on every comment that says it has any.
//
// All of them at once rather than the selected one, because a reaction is part
// of reading a thread, not part of acting on it — waiting to select a comment to
// find out it was boosted is finding out too late. The API lists them one
// recording at a time, so this is one request per comment; the count on each
// comment is what keeps that to the comments that actually have some, which on
// most threads is a handful.
func (l *commentList) readBoosts() tea.Cmd {
	reads := make([]tea.Cmd, 0, len(l.comments))
	for _, answer := range l.comments {
		if answer.boosted == 0 {
			continue
		}
		if _, read := l.boosts[answer.id]; read {
			continue
		}
		reads = append(reads, loadBoosts(l.ctx.Ctx(), l.ctx.app, l.me, answer.id))
	}
	if len(reads) == 0 {
		return nil
	}
	return tea.Batch(reads...)
}

// --- Walking them ---

// The keys a comment list answers. Walking with j and k rather than the arrows:
// the arrows scroll whatever the comments are under, and a reader who has come
// down to the comments is stepping between them rather than scrolling.
const (
	nextCommentKey = "j"
	prevCommentKey = "k"
	boostKey       = "b"
)

// handleKey walks the list and offers what can be done to the comment under the
// cursor, and answers whether it took the key.
//
// Every screen with comments under it routes keys through here first, so enter
// on a comment means the same thing wherever it is read. What a screen does not
// hand over — scrolling, and whatever the recording itself answers — it keeps.
func (l *commentList) handleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case nextCommentKey:
		return nil, l.moveCursor(1)
	case prevCommentKey:
		return nil, l.moveCursor(-1)
	case boostKey:
		return l.openBoosts(), true
	}
	if msg.Key().Code == tea.KeyEnter {
		return l.openActions(), true
	}
	return nil, false
}

// openActions offers what can be done to the selected comment. Nothing selected
// is not an error: the reader is on the recording itself, which has actions of
// its own that are not these.
func (l *commentList) openActions() tea.Cmd {
	selected, standing := l.selected()
	if !standing {
		return nil
	}
	return func() tea.Msg {
		return commentActionsMsg{comment: selected, mine: selected.mine}
	}
}

// openBoosts shows every reaction on the selected comment, with whoever left
// each one named in full.
//
// Its own key rather than a row in the actions, because boosting is the one
// thing a reader does to a comment over and over, and two key presses deep makes
// the commonest thing the rarest.
func (l *commentList) openBoosts() tea.Cmd {
	selected, standing := l.selected()
	if !standing {
		return nil
	}
	boosts := l.boosts[selected.id]
	return func() tea.Msg {
		return boostMenuMsg{comment: selected, boosts: boosts}
	}
}

// helpBindings are the list's own entries in the help bar, for a screen to add
// to its.
func (l *commentList) helpBindings() []helpBinding {
	return []helpBinding{
		{nextCommentKey + prevCommentKey, "comment"},
		{"enter", "comment actions"},
		{boostKey, "boosts"},
	}
}

// selected is the comment under the cursor, and false when the cursor is on the
// recording itself.
func (l *commentList) selected() (comment, bool) {
	if l.cursor < 0 || l.cursor >= len(l.comments) {
		return comment{}, false
	}
	return l.comments[l.cursor], true
}

// moveCursor walks the list and answers whether it moved. The cursor starts
// above the first comment, on the recording itself, and -1 is where up puts it
// back.
func (l *commentList) moveCursor(by int) bool {
	was := l.cursor
	l.cursor = max(min(l.cursor+by, len(l.comments)-1), -1)
	return l.cursor != was
}

func (l *commentList) clampCursor() {
	l.cursor = max(min(l.cursor, len(l.comments)-1), -1)
}

// people is everyone who has said something, for a screen reading their
// pictures.
func (l *commentList) people() []string {
	sources := make([]string, 0, len(l.comments))
	for _, answer := range l.comments {
		sources = append(sources, answer.author.avatar)
	}
	return sources
}

// carried is every picture the comments themselves hold, as one body, so a
// screen can read them all in the order they were said.
func (l *commentList) carried() body {
	all := body{sources: map[string]string{}}
	for _, answer := range l.comments {
		all.parts = append(all.parts, answer.words.parts...)
		for at, from := range answer.words.sources {
			all.sources[at] = from
		}
	}
	return all
}

// --- Rendering ---

// rows is the whole list drawn: each comment, with a rule between two of them.
//
// shown is the screen's pictures — the faces beside the names and whatever the
// comments carry. focused says the cursor is in the list rather than somewhere
// else on the screen, which is what decides whether the selected comment is
// marked at all.
func (l *commentList) rows(styles *tui.Styles, shown *pictures,
	width int, now time.Time, focused bool) []string {
	switch {
	case l.notice != "":
		return wrapText(l.notice, width)
	case l.reading:
		return []string{styles.Muted.Render("Loading…")}
	case len(l.comments) == 0:
		return []string{styles.Muted.Render("  No comments yet.")}
	}

	var rows []string
	for index, answer := range l.comments {
		if index > 0 {
			// A rule between comments, the way the web separates its cards:
			// without one a two-line comment runs straight into the next
			// person's name.
			rows = append(rows, "", styles.Muted.Render(strings.Repeat("─", max(width, 1))), "")
		}
		rows = append(rows, l.commentRows(styles, answer, shown, width, now, focused && index == l.cursor)...)
	}
	return rows
}

// commentRows is one comment: who wrote it beside their picture, then what they
// said under both, then the reactions left on it.
func (l *commentList) commentRows(styles *tui.Styles, answer comment, shown *pictures,
	width int, now time.Time, selected bool) []string {
	theme := styles.Theme()
	name := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)
	if selected {
		name = name.Foreground(theme.Primary)
	}

	face := shown.face(answer.author.avatar)
	inner := max(width-2, 1)
	if len(face) > 0 {
		inner = max(inner-avatarCols-2, 1)
	}

	said := name.Render(truncateToWidth(answer.author.name, inner))
	if aside := byline("", answer.author.title, since(answer.at, now)); aside != "" {
		room := max(inner-tui.DisplayWidth(answer.author.name), 1)
		said += styles.Muted.Render(truncateToWidth(" · "+aside, room))
	}

	// The reactions go inside the block, not after it, so they line up under the
	// words they are reacting to rather than under the face.
	block := append([]string{said}, shown.rows(answer.words, styles, inner)...)
	if left := boostRows(styles, shown, l.boosts[answer.id], inner); len(left) > 0 {
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

// selectedMarker stands in the left margin of the comment the reader is on. It
// is also how a screen finds that comment's rows when it scrolls to one, so it
// appears nowhere else.
const selectedMarker = "▌"

// span is where a comment's rows start and end, which is what scrolling to one
// needs to know.
type span struct{ first, last int }

// markedSpan finds the selected comment in a screen that has already been laid
// out.
//
// The marker is what says which rows are its. Asking the drawing rather than
// counting the rows a second time is the point: two answers to where a comment
// landed is one answer too many, and the layout is the one that is on screen.
func markedSpan(rows []string) span {
	at := span{first: -1, last: -1}
	for index, row := range rows {
		if !strings.Contains(row, selectedMarker) {
			continue
		}
		if at.first < 0 {
			at.first = index
		}
		at.last = index
	}
	return at
}

// scrollTo brings a marked span onto a screen of some height, from wherever it
// is scrolled to now.
func scrollTo(at span, offset, height, rows int) int {
	if at.first < 0 || height <= 0 {
		return max(min(offset, max(rows-height, 0)), 0)
	}
	offset = min(offset, at.first)
	if at.last >= offset+height {
		offset = at.last - height + 1
	}
	return max(min(offset, max(rows-height, 0)), 0)
}

// byline joins what is said about a person beside their name — their title, when
// they wrote, the category — dropping whichever of them is missing.
func byline(who, title string, rest ...string) string {
	return strings.Join(nonEmpty(append([]string{who, title}, rest...)...), " · ")
}

// --- Reading ---

func loadComments(ctx context.Context, app *appctx.App, recording int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return commentsMsg{recording: recording, err: err}
		}

		result, err := app.Account().Comments().List(ctx, recording, &basecamp.CommentListOptions{
			Limit: commentPageLimit,
		})
		if err != nil {
			return commentsMsg{recording: recording, err: err}
		}

		// Who the reader is decides which comments they may edit or trash. A
		// failure here is not the comments' failure: they are still worth
		// showing, with nothing marked as the reader's own.
		var me int64
		if who, err := app.Account().People().Me(ctx); err == nil && who != nil {
			me = who.ID
		}

		comments := make([]comment, 0, len(result.Comments))
		for _, said := range result.Comments {
			comments = append(comments, toComment(said, me))
		}
		return commentsMsg{recording: recording, me: me, comments: comments}
	}
}

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

func toComment(said basecamp.Comment, me int64) comment {
	out := comment{
		id:      said.ID,
		words:   newBody(said.Content, said.ContentAttachments),
		at:      said.CreatedAt.Local(),
		url:     said.AppURL,
		boosted: said.BoostsCount,
	}
	if said.Creator != nil {
		out.author = toPerson(said.Creator)
		out.mine = me != 0 && said.Creator.ID == me
	}
	return out
}
