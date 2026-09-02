package workspace

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// messageBoardKind is the dock's own name for a project's message board, which
// is how the tool arrives from the API.
const messageBoardKind = "message_board"

// How many messages a page of the board asks for. The board is a list of
// subjects rather than a feed of bodies, so a page holds a screenful several
// times over and the walk rarely runs.
const messageBoardPageSize = 25

// message is one post on a board, flattened to what the list and the screen
// behind it both need. The body comes along because the API's index carries it —
// api/messages/_message.json.jbuilder renders the rich text — so opening a
// message costs no second read.
type message struct {
	id      int64
	subject string
	body    string
	author  person

	// kind is the category's name, which is what the board's rows show.
	// categoryID is the same category, which is what an edit sends back.
	kind       string
	categoryID int64

	// bucket is the project it was posted in, which is where its board's
	// categories come from.
	bucket int64

	// draft is a message only its writer can see, which is why the board shows
	// them above what is posted: they are unfinished business.
	draft bool

	comments int

	// images maps a picture's address in the body to the one it can be read
	// from. See imageSources.
	images map[string]string

	// at is local time, for the same reason activity keeps the instant rather
	// than a worded date: how long ago something was posted changes every minute.
	at time.Time
}

// openMessageMsg asks the model for the screen behind one post on the board.
type openMessageMsg struct{ message message }

// messagePageMsg is a page of a board.
type messagePageMsg struct {
	page     int
	messages []message
	err      error
}

// messageDraftsMsg is the reader's own unposted messages on this board.
type messageDraftsMsg struct {
	drafts []message
	err    error
}

// messageReadMsg is one message read in full, on its way to being opened.
type messageReadMsg struct {
	message message
	err     error
}

// messageBoardScreen is a project's message board: its posts, newest first, and
// a way into any of them.
//
// The web draws each post as a card with the first few lines of its body showing.
// A terminal has one column instead of three, so a post is two lines here — the
// subject, then who wrote it and how many have replied — and the body waits until
// it is opened. Twenty subjects a reader can scan beats five they have to scroll.
type messageBoardScreen struct {
	ctx *Context

	// board is the tool this screen was opened from: its id is what gets read,
	// and its name is what the breadcrumb says. inside is the project it hangs
	// off, whose categories a new message may take.
	board  tool
	inside project

	// drafts are the reader's own, above the posted messages. messages are the
	// board itself, a page at a time.
	drafts   []message
	messages []message
	page     int
	paging   bool
	// opening is a draft being read: the drafts list carries an excerpt rather
	// than a body, so choosing one asks for the message itself.
	opening bool
	// done is the end of the board; stalled is a page that did not arrive, which
	// the next key press retries.
	done    bool
	stalled bool
	notice  string

	cursor int
	offset int
	width  int
	height int

	now func() time.Time
}

func newMessageBoard(ctx *Context, board tool, inside project) *messageBoardScreen {
	return &messageBoardScreen{ctx: ctx, board: board, inside: inside, now: time.Now}
}

func (b *messageBoardScreen) Init() tea.Cmd {
	b.drafts, b.messages, b.page, b.done, b.notice = nil, nil, 0, false, ""
	b.opening = false
	return tea.Batch(b.readMore(), loadMessageDrafts(b.ctx.Ctx(), b.ctx.app, b.board.id))
}

func (b *messageBoardScreen) Title() string { return b.board.name }

func (b *messageBoardScreen) Loading() bool { return b.opening }

// posts is what the cursor walks: the reader's own drafts first, then the board.
func (b *messageBoardScreen) posts() []message {
	return append(append([]message{}, b.drafts...), b.messages...)
}

func (b *messageBoardScreen) Resize(width, height int) {
	b.width = width
	b.height = height
	b.scrollToCursor()
}

func (b *messageBoardScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case messageDraftsMsg:
		// Drafts that could not be read leave the board as it was. They are a
		// second read for the reader's own unfinished writing, not the board.
		if msg.err == nil {
			b.drafts = msg.drafts
			b.scrollToCursor()
		}
		return nil, true

	case messageReadMsg:
		b.opening = false
		if msg.err != nil {
			return notifyError("Could not open the draft", msg.err), true
		}
		// A draft opens in the form it was written in rather than as something to
		// read. The web does the same: a drafted message's page is its edit form,
		// and the link to it says "Continue writing your draft".
		opened := msg.message
		return func() tea.Msg { return editMessageMsg{message: opened} }, true
	}

	page, ok := msg.(messagePageMsg)
	if !ok {
		return nil, false
	}

	b.paging = false
	if page.err != nil {
		// What is on screen is still good, so a page that failed to arrive leaves
		// it alone. The walk stalls rather than ending, the way the feed's does:
		// a list that stops dead on one dropped request reads as the end of the
		// board. The next key press tries again.
		b.stalled = true
		if len(b.messages) == 0 {
			b.notice = errorNotice("Could not load the messages", page.err)
		}
		return nil, true
	}

	b.stalled = false
	if len(page.messages) == 0 {
		b.done = true
		return nil, true
	}

	b.page = page.page
	b.messages = append(b.messages, page.messages...)
	b.scrollToCursor()
	return nil, true
}

func (b *messageBoardScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyUp:
		b.cursor = max(b.cursor-1, 0)
	case tea.KeyDown:
		b.cursor = min(b.cursor+1, max(len(b.posts())-1, 0))
	case tea.KeyEnter:
		return b.open()
	default:
		if msg.String() == newMessageKey {
			return b.write()
		}
		return nil
	}

	b.scrollToCursor()
	return b.readMore()
}

// open is what enter does on the cursor's row. A draft is read first: the drafts
// list carries a plain-text excerpt of one, and it opens for writing rather than
// reading, so what it needs is the body.
func (b *messageBoardScreen) open() tea.Cmd {
	posts := b.posts()
	if b.cursor >= len(posts) {
		return nil
	}

	chosen := posts[b.cursor]
	if chosen.draft {
		b.opening = true
		return loadMessage(b.ctx.Ctx(), b.ctx.app, chosen.id)
	}
	return func() tea.Msg { return openMessageMsg{message: chosen} }
}

// write opens the form for a new message on this board.
func (b *messageBoardScreen) write() tea.Cmd {
	board, bucket := b.board, b.inside.id
	return func() tea.Msg { return newMessageMsg{board: board, bucket: bucket} }
}

// readMore asks for the page below what is loaded, when the cursor has come near
// the end of it or there is not enough to fill the screen.
func (b *messageBoardScreen) readMore() tea.Cmd {
	if b.paging || b.done {
		return nil
	}
	posts := len(b.posts())
	if posts > 0 && posts > b.height && b.cursor < posts-pageAheadBy {
		return nil
	}

	b.paging = true
	return loadMessagePage(b.ctx.Ctx(), b.ctx.app, b.board.id, b.page+1)
}

func (b *messageBoardScreen) scrollToCursor() {
	if b.height <= 0 {
		b.offset = 0
		return
	}

	// A message is two lines, and both of them are the message: scrolling to its
	// first line and stopping there leaves the second one clipped off the bottom.
	rows := b.layout()
	first, last := -1, -1
	for index, row := range rows {
		if row.item == b.cursor {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first < 0 {
		b.offset = 0
		return
	}

	b.offset = min(b.offset, topOf(rows, first))
	if last >= b.offset+b.height {
		b.offset = last - b.height + 1
	}
	b.offset = max(min(b.offset, max(len(rows)-b.height, 0)), 0)
}

// --- Rendering ---

func (b *messageBoardScreen) View() string {
	if b.notice != "" {
		return strings.Join(wrapText(b.notice, b.width), "\n")
	}

	rows := b.layout()
	end := min(b.offset+b.height, len(rows))
	lines := make([]string, 0, max(end-b.offset, 0))
	for _, row := range rows[min(b.offset, end):end] {
		lines = append(lines, row.text)
	}
	return strings.Join(lines, "\n")
}

func (b *messageBoardScreen) layout() []homeRow {
	styles := b.ctx.Styles()

	var rows []homeRow
	plain := func(text string) { rows = append(rows, homeRow{text: text, item: noItem}) }
	item := func(text string, at int) { rows = append(rows, homeRow{text: text, item: at}) }

	posts := b.posts()
	switch {
	case len(posts) == 0 && b.paging:
		plain(styles.Muted.Render("Loading…"))
		return rows
	case len(posts) == 0 && b.stalled:
		plain(lipgloss.NewStyle().Foreground(styles.Theme().Error).
			Render("Could not load the messages. Press ↓ to try again."))
		return rows
	case len(posts) == 0:
		plain(styles.Muted.Render("Nothing posted yet."))
		return rows
	}

	now := b.now()
	for index, post := range posts {
		for _, line := range messageRows(styles, post, now, b.width, index == b.cursor) {
			item(line, index)
		}
	}

	plain("")
	plain(b.footer())
	return rows
}

func (b *messageBoardScreen) footer() string {
	styles := b.ctx.Styles()
	switch {
	case b.paging:
		return styles.Muted.Render("Loading more…")
	case b.stalled:
		return lipgloss.NewStyle().Foreground(styles.Theme().Error).
			Render("Could not load more. Press ↓ to try again.")
	case b.done:
		return styles.Muted.Render("That's everything.")
	default:
		return ""
	}
}

func (b *messageBoardScreen) HelpBindings() []helpBinding {
	return []helpBinding{{"↑↓", "move"}, {"enter", "read"}, {"a", "new message"}}
}

// messageRows is one post: its subject, then who wrote it and how many have
// replied, with the time down the same left-hand column the feeds use.
func messageRows(styles *tui.Styles, post message, now time.Time, width int, selected bool) []string {
	theme := styles.Theme()

	marker := "  "
	subject := lipgloss.NewStyle().Foreground(theme.Foreground)
	if selected {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		subject = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	inner := max(width-2-gutterWidth-1, 1)

	// No marker for a pinned post: the web floats one to the top of the board,
	// but the API never says which posts are pinned. Recording::Pinnable#pinned?
	// exists on the model and api/recordings/_recording.json.jbuilder does not
	// render it. See "[API] Expose pinned on a recording" on the CLIs board.
	return []string{
		marker + styles.Muted.Render(gutter(since(post.at, now))) + " " +
			subject.Render(truncateToWidth(post.subject, inner)),
		"  " + styles.Muted.Render(gutter(clockOf(post.at))) + " " +
			styles.Muted.Render(truncateToWidth(post.byline(), inner)),
	}
}

// byline is the second line's account of a post: who wrote it, how many have
// answered, and its category. A draft says so instead — it is the reader's own
// and nobody has replied to something nobody else can see.
func (m message) byline() string {
	if m.draft {
		return "Draft"
	}
	return strings.Join(nonEmpty(m.author.name, m.replies(), m.kind), " · ")
}

// replies is how the second line says how many have answered, or nothing at all
// when nobody has.
func (m message) replies() string {
	switch m.comments {
	case 0:
		return ""
	case 1:
		return "1 reply"
	default:
		return strconv.Itoa(m.comments) + " replies"
	}
}

// --- Reading ---

func loadMessagePage(ctx context.Context, app *appctx.App, boardID int64, page int) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messagePageMsg{page: page, err: err}
		}

		result, err := app.Account().Messages().List(ctx, boardID, &basecamp.MessageListOptions{
			Page:  page,
			Limit: messageBoardPageSize,
		})
		if err != nil {
			return messagePageMsg{page: page, err: err}
		}

		messages := make([]message, 0, len(result.Messages))
		for _, post := range result.Messages {
			messages = append(messages, toMessage(post))
		}
		return messagePageMsg{page: page, messages: messages}
	}
}

// loadMessageDrafts reads the reader's own unposted messages and keeps the ones
// filed under this board.
//
// Basecamp lists a person's drafts across every active project — there is no
// per-board listing, only a count — so the filter is here. What comes back is a
// title and an excerpt rather than a message, which is why choosing one reads it.
func loadMessageDrafts(ctx context.Context, app *appctx.App, boardID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messageDraftsMsg{err: err}
		}
		result, err := app.Account().Drafts().List(ctx, 0)
		if err != nil {
			return messageDraftsMsg{err: err}
		}

		var drafts []message
		for _, unposted := range result.Drafts {
			if unposted.Parent == nil || unposted.Parent.ID != boardID {
				continue
			}
			drafts = append(drafts, toDraft(unposted))
		}
		return messageDraftsMsg{drafts: drafts}
	}
}

// loadMessage reads one message, which is what opening a draft needs: the drafts
// list carries an excerpt of the body rather than the body.
func loadMessage(ctx context.Context, app *appctx.App, messageID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return messageReadMsg{err: err}
		}
		post, err := app.Account().Messages().Get(ctx, messageID)
		if err != nil {
			return messageReadMsg{err: err}
		}
		return messageReadMsg{message: toMessage(*post)}
	}
}

func toDraft(unposted basecamp.Draft) message {
	return message{
		id:      unposted.ID,
		subject: unposted.Title,
		body:    unposted.Excerpt,
		draft:   true,
		// When it was last written, not when it was started: a draft is something
		// being worked on, and the board sorts them by the same clock the web does.
		at: unposted.UpdatedAt.Local(),
	}
}

func toMessage(post basecamp.Message) message {
	out := message{
		id:       post.ID,
		subject:  post.Subject,
		body:     strings.TrimSpace(richtext.HTMLToMarkdown(post.Content)),
		images:   imageSources(post.ContentAttachments),
		comments: post.CommentsCount,
		draft:    post.Status == messageDrafted,
		at:       post.CreatedAt.Local(),
	}
	if post.Creator != nil {
		out.author = toPerson(post.Creator)
	}
	if post.Bucket != nil {
		out.bucket = post.Bucket.ID
	}
	// A board can be set up with categories — "Announcement", "FYI" — which the
	// web shows as a label on the card. Not every board has them.
	if post.Category != nil {
		out.kind, out.categoryID = post.Category.Name, post.Category.ID
	}
	if post.Subject == "" {
		// A message posted without a subject is titled by the API instead. The web
		// shows that, so there is always something on the first line.
		out.subject = post.Title
	}
	return out
}

// imageSources maps a picture's address in the body to the address it can be read
// from.
//
// The body's Markdown points at the preview host, which is where the browser
// reads a picture from and is not somewhere this will: accountImageReader reads
// from the account's API host and nowhere else. The same attachment appears in
// content_attachments with a download_url that is on that host, so the body says
// one address and the read uses the other.
func imageSources(attachments []basecamp.RichTextAttachment) map[string]string {
	sources := make(map[string]string, len(attachments))
	for _, file := range attachments {
		if !strings.HasPrefix(strings.ToLower(file.ContentType), "image/") || file.DownloadURL == "" {
			continue
		}
		if file.PreviewURL != "" {
			sources[file.PreviewURL] = file.DownloadURL
		}
		sources[file.DownloadURL] = file.DownloadURL
	}
	return sources
}
