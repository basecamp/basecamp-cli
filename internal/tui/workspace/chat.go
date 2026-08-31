package workspace

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/markdown"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// chatKind is the dock's own name for a project's chat, which is how the
	// project screen knows this is the screen behind that tool.
	chatKind = "chat"

	// The key that opens the composer. Enter does too — there is nothing else to
	// open on this screen — but a chat is a place you type, so it has a letter of
	// its own.
	composeKey = "c"

	// The key that reads the newest page again. Nothing pushes new lines here
	// yet, so this is how a reader catches up.
	refreshKey = "r"

	// composerMaxRows is how tall the composer grows before it starts scrolling
	// instead. A message longer than five lines is a message board post.
	composerMaxRows = 5
)

// chatLine is one thing somebody said, flattened to what a terminal can print.
// The body arrives as rich text and is converted once, on the way in, so
// rendering stays a pure function of what was read.
type chatLine struct {
	id   int64
	who  string
	body string
	at   time.Time

	// imageURL is where the picture the line carries can be read, and empty when
	// nothing about the line is a picture. imageData is that picture once it has
	// arrived, kept so it can be redrawn at another size without asking again, and
	// image is it drawn.
	imageURL  string
	imageData []byte
	image     tui.RenderedImage
}

// chatPageMsg is a page of the transcript. Basecamp answers newest first and
// pages backwards, so a higher page number is older.
type chatPageMsg struct {
	page  int
	lines []chatLine
	err   error
}

// chatSaidMsg is the line the reader just sent, back from the server with its id
// and timestamp.
type chatSaidMsg struct {
	line chatLine
	err  error
}

// chatScreen is a project's chat: the transcript, oldest at the top, and a
// composer under it.
//
// It does not update itself. Basecamp pushes new lines over ActionCable to a
// channel our OAuth client is not yet trusted to join, so there is nothing to
// listen to; `r` re-reads. When that trust arrives the stream is a doorbell —
// ring, re-read the newest page — which is the same shape as ReadingsWatcher in
// live.go. The HTML it carries is beside the point: what a client needs from it
// is the news that something changed.
type chatScreen struct {
	ctx  *Context
	tool tool

	// lines are oldest first, which is the order they are read in. A page of
	// older ones goes on the front.
	lines []chatLine
	page  int

	paging  bool
	done    bool
	stalled bool
	notice  string

	// The composer, and whether the reader is typing in it. A text area rather
	// than a field: a message can be a paragraph, and Markdown needs the lines.
	compose textarea.Model
	writing bool
	sending bool

	// fromBottom is how far up from the newest line the window sits, in rows.
	// Measured from the bottom rather than the top because that is the end a
	// chat is read from: older pages land above the window and leave it alone.
	fromBottom int

	// How a picture gets drawn, what this screen may spend on pictures, and
	// whether it has already said that this terminal draws none.
	images         tui.ImageRenderer
	budget         *imageBudget
	saidNoPictures bool

	width  int
	height int

	now func() time.Time
}

func newChat(ctx *Context, open tool) *chatScreen {
	return &chatScreen{
		ctx:     ctx,
		tool:    open,
		compose: newComposer(),
		images:  tui.NewImageRenderer(),
		budget:  newImageBudget(),
		now:     time.Now,
	}
}

// newComposer is the field a message is written in. It grows with what is typed
// and stops growing at composerMaxRows, so a short message takes one line and the
// transcript keeps the rest of the screen.
func newComposer() textarea.Model {
	compose := textarea.New()
	compose.Prompt = ""
	compose.ShowLineNumbers = false
	compose.Placeholder = "Write a message… Markdown works here"
	compose.DynamicHeight = true
	compose.MinHeight = 1
	compose.MaxHeight = composerMaxRows
	compose.SetHeight(1)
	compose.SetStyles(composerStyles())
	return compose
}

// composerStyles leave the text alone. What is being typed is Markdown, and
// styleInlineMarkdown is what says so — a band under the cursor line or a color on
// the text itself would fight it. The cursor says where the cursor is.
func composerStyles() textarea.Styles {
	plain := lipgloss.NewStyle()
	muted := lipgloss.NewStyle().Faint(true)

	focused := textarea.StyleState{
		Base:             plain,
		Text:             plain,
		LineNumber:       muted,
		CursorLineNumber: plain,
		CursorLine:       plain,
		EndOfBuffer:      muted,
		Placeholder:      muted,
		Prompt:           plain,
		Selection:        lipgloss.NewStyle().Reverse(true),
	}
	return textarea.Styles{Focused: focused, Blurred: focused}
}

func (c *chatScreen) Init() tea.Cmd {
	c.lines, c.page, c.done, c.stalled, c.notice = nil, 0, false, false, ""
	c.fromBottom = 0
	c.budget = newImageBudget()
	return c.readMore()
}

func (c *chatScreen) Title() string { return c.tool.name }

func (c *chatScreen) Loading() bool { return false }

func (c *chatScreen) Resize(width, height int) {
	c.width = width
	c.height = height
	c.compose.SetWidth(max(width-2, 1))
}

// CapturingInput is true while the composer has the keys. A message is text, so
// while one is being written every key belongs to it — the section digits and
// the menu chord included.
func (c *chatScreen) CapturingInput() bool { return c.writing }

// HandleBack closes the composer rather than letting esc pop the screen: the
// composer is what the reader opened last, so it is what esc closes. A message
// half written is kept, so esc is not a way to lose it.
func (c *chatScreen) HandleBack() bool {
	if !c.writing {
		return false
	}
	c.stopWriting()
	return true
}

func (c *chatScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case chatPageMsg:
		c.pageArrived(msg)
		// A page is fifteen lines, which on a tall terminal is half a screen. Keep
		// asking until the screen is full or the conversation runs out, rather than
		// leaving the reader to press ↑ at a gap.
		return tea.Batch(c.readMore(), c.readImages()), true

	case chatImagesMsg:
		return c.drawImages(msg.images), true

	case chatImagesPlacedMsg:
		c.placeImages(msg.drawn)
		return nil, true

	case chatSaidMsg:
		c.sending = false
		if msg.err != nil {
			// The text is still in the composer, so the reader can try again
			// rather than write it twice.
			return notifyError("Could not send the message", msg.err), true
		}
		c.append(msg.line)
		return nil, true
	}

	if c.writing {
		compose, cmd := c.compose.Update(msg)
		c.compose = compose
		return cmd, false
	}
	return nil, false
}

func (c *chatScreen) pageArrived(msg chatPageMsg) {
	c.paging = false
	if msg.err != nil {
		// What is on screen is still good, so a page that failed to arrive
		// leaves it alone. The walk stalls rather than ending: the next scroll
		// up tries again.
		c.stalled = true
		if len(c.lines) == 0 {
			c.notice = errorNotice("Could not load the chat", msg.err)
		}
		return
	}

	c.stalled = false
	if len(msg.lines) == 0 {
		c.done = true
		return
	}

	c.page = msg.page
	// The page arrives newest first and belongs above what is already here.
	c.lines = append(reversed(msg.lines), c.lines...)
}

// append puts a line the reader just sent at the newest end, and follows it
// down: they wrote it, so they want to see it.
func (c *chatScreen) append(line chatLine) {
	c.lines = append(c.lines, line)
	c.fromBottom = 0
}

// readImages asks for the pictures the transcript is carrying and has not read
// yet. Nothing to read, or nothing left to spend, and no read is started.
func (c *chatScreen) readImages() tea.Cmd {
	if c.images == nil || c.budget.spent() {
		return nil
	}
	wanted := wantedImages(c.lines)
	if len(wanted) == 0 {
		return nil
	}
	// A terminal that cannot draw one is not asked to read one — but it does say
	// so, once. A filename where a picture should be is otherwise indistinguishable
	// from a picture that failed to arrive, which is a thing to know about your own
	// terminal rather than to wonder about.
	if c.images.Protocol() == tui.ImageProtocolText {
		return c.sayPicturesAreNotDrawn()
	}
	return loadChatImages(c.ctx.Ctx(), c.ctx.app, c.budget, wanted)
}

func (c *chatScreen) sayPicturesAreNotDrawn() tea.Cmd {
	if c.saidNoPictures {
		return nil
	}
	c.saidNoPictures = true
	return notify("This terminal can't show pictures — Kitty and Ghostty can")
}

// drawImages renders the pictures that arrived, sends the terminal their pixels,
// and only then asks for their cells to go on the lines. That order is the whole
// job.
//
// An image is two things sent two ways: the pixels, which go straight out through
// tea.Raw, and the cells that stand where the image should appear, which go in a
// frame like any other row. The cells are a reference — each one says "row R,
// column C of image N" — so a terminal that has not been given image N draws
// nothing at all for them.
//
// Bubble Tea paints the frame immediately after an update and runs that update's
// commands afterwards. Putting the cells on their lines here would show them a
// frame before the pixels went out, and since nothing changes after that, the
// renderer never repaints those rows and the picture never arrives. tea.Sequence
// is what keeps the two in order: the pixels are written, then the cells appear.
//
// The size is settled here, against the column as it is now. It is not redrawn on
// a resize: the cells are what the terminal matches the image against, so a
// narrower window leaves the picture out rather than showing part of one — see
// lineRows.
func (c *chatScreen) drawImages(arrived map[int64][]byte) tea.Cmd {
	drawn := make(map[int64]tui.RenderedImage, len(arrived))
	var pixels strings.Builder

	for index, line := range c.lines {
		data, ok := arrived[line.id]
		if !ok || len(data) == 0 {
			continue
		}
		rendered := c.images.Render(data, tui.NextImageID(), c.bodyWidth())
		if rendered.Content == "" {
			continue
		}
		// The data is kept on the line now rather than with the cells, so nothing
		// asks for this picture again while its pixels are still on their way out.
		c.lines[index].imageData = data
		drawn[line.id] = rendered
		pixels.WriteString(rendered.Raw)
	}

	if len(drawn) == 0 {
		return nil
	}
	return tea.Sequence(tea.Raw(pixels.String()), placeImages(drawn))
}

// placeImages is the second half of drawing: the terminal has the pixels, so the
// cells that point at them can go on screen.
func placeImages(drawn map[int64]tui.RenderedImage) tea.Cmd {
	return func() tea.Msg { return chatImagesPlacedMsg{drawn: drawn} }
}

// chatImagesPlacedMsg carries pictures whose pixels the terminal already holds.
type chatImagesPlacedMsg struct {
	drawn map[int64]tui.RenderedImage
}

func (c *chatScreen) placeImages(drawn map[int64]tui.RenderedImage) {
	for index, line := range c.lines {
		if rendered, ok := drawn[line.id]; ok {
			c.lines[index].image = rendered
		}
	}
}

func reversed(lines []chatLine) []chatLine {
	flipped := make([]chatLine, 0, len(lines))
	for index := len(lines) - 1; index >= 0; index-- {
		flipped = append(flipped, lines[index])
	}
	return flipped
}

// --- Keys ---

func (c *chatScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if c.writing {
		return c.handleWritingKey(msg)
	}

	switch {
	case msg.String() == composeKey, msg.Key().Code == tea.KeyEnter:
		return c.startWriting()
	case msg.String() == refreshKey:
		return c.Init()
	case msg.Key().Code == tea.KeyUp:
		return c.scroll(1)
	case msg.Key().Code == tea.KeyDown:
		return c.scroll(-1)
	case msg.Key().Code == tea.KeyPgUp:
		return c.scroll(max(c.transcriptHeight()-1, 1))
	case msg.Key().Code == tea.KeyPgDown:
		return c.scroll(-max(c.transcriptHeight()-1, 1))
	}
	return nil
}

func (c *chatScreen) startWriting() tea.Cmd {
	c.writing = true
	c.fromBottom = 0
	return c.compose.Focus()
}

func (c *chatScreen) stopWriting() {
	c.writing = false
	c.compose.Blur()
}

func (c *chatScreen) handleWritingKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.Key()
	switch {
	case key.Code == tea.KeyEsc:
		c.stopWriting()
		return nil
	// Enter sends, the way it does in a chat. A message that wants a second line
	// asks for one with alt: shift+enter is what a browser uses, but a terminal
	// only reports it when the keyboard protocol is on, so it is taken as well
	// rather than relied on.
	case key.Code == tea.KeyEnter && key.Mod&(tea.ModAlt|tea.ModShift) != 0:
		c.compose.InsertString("\n")
		return nil
	case key.Code == tea.KeyEnter:
		return c.send()
	}

	compose, cmd := c.compose.Update(msg)
	c.compose = compose
	return cmd
}

// send posts what was typed. The composer stays open: a chat is a conversation,
// and the next thing to do after saying something is usually saying something
// else.
func (c *chatScreen) send() tea.Cmd {
	said := strings.TrimSpace(c.compose.Value())
	if said == "" || c.sending {
		return nil
	}

	c.sending = true
	c.compose.SetValue("")
	return sayInChat(c.ctx.Ctx(), c.ctx.app, c.tool.id, said)
}

// scroll moves the window up through older lines, and asks for the page above
// when it gets near the oldest one that has arrived.
func (c *chatScreen) scroll(by int) tea.Cmd {
	rows := c.rows()
	c.fromBottom = max(min(c.fromBottom+by, max(len(rows)-c.transcriptHeight(), 0)), 0)
	return c.readMore()
}

// readMore asks for the page above what is loaded, when the window has come near
// the top of it or there is not enough to fill the screen.
func (c *chatScreen) readMore() tea.Cmd {
	if c.paging || c.done {
		return nil
	}

	// Ask when the window is near the oldest line that has arrived, or when
	// there is not enough to fill the screen.
	rows := c.rows()
	if len(rows) > c.transcriptHeight() && len(rows)-c.fromBottom-c.transcriptHeight() > pageAheadBy {
		return nil
	}

	c.paging = true
	return loadChatPage(c.ctx.Ctx(), c.ctx.app, c.tool.id, c.page+1)
}

// --- Rendering ---

// transcriptHeight is the room left for the transcript once the composer has
// taken its rows. The composer grows with what is being typed, so this shrinks as
// a longer message is written.
func (c *chatScreen) transcriptHeight() int {
	return max(c.height-len(c.composer()), 1)
}

func (c *chatScreen) View() string {
	if c.notice != "" {
		return strings.Join(wrapText(c.notice, c.width), "\n")
	}

	rows := c.rows()
	end := max(len(rows)-c.fromBottom, 0)
	start := max(end-c.transcriptHeight(), 0)

	lines := make([]string, 0, c.height)
	// A conversation shorter than the screen sits on the composer rather than
	// hanging from the top of it, the way it does everywhere else.
	for range max(c.transcriptHeight()-(end-start), 0) {
		lines = append(lines, "")
	}
	lines = append(lines, rows[start:end]...)
	return strings.Join(append(lines, c.composer()...), "\n")
}

// rows is the whole transcript as drawn lines: a rule for each new day, then
// each line's own rows.
func (c *chatScreen) rows() []string {
	rows := make([]string, 0, len(c.lines)*2+4)
	rows = append(rows, c.header()...)

	now := c.now()
	day := time.Time{}
	previous := chatLine{}
	for index, line := range c.lines {
		if !sameDay(line.at, day) {
			day = line.at
			if index > 0 {
				rows = append(rows, "")
			}
			rows = append(rows, c.dayHeading(line.at, now))
			previous = chatLine{}
		}
		rows = append(rows, c.lineRows(line, previous)...)
		previous = line
	}
	return rows
}

// header says how far back the transcript reaches, so the top of it is never
// just a conversation that started there.
func (c *chatScreen) header() []string {
	styles := c.ctx.Styles()
	switch {
	case c.paging && len(c.lines) == 0:
		return []string{styles.Muted.Render("Loading…")}
	case len(c.lines) == 0:
		return []string{styles.Muted.Render("Nothing said here yet.")}
	case c.stalled:
		return []string{lipgloss.NewStyle().Foreground(styles.Theme().Error).
			Render("Could not load more. Press ↑ to try again.")}
	case c.paging:
		return []string{styles.Muted.Render("Loading more…")}
	case c.done:
		return []string{styles.Muted.Render("The beginning of the chat.")}
	default:
		return nil
	}
}

// lineRows is one message: who said it and when, then what they said. A second
// message from the same person in the same minute skips the name — it is the
// same person still talking.
func (c *chatScreen) lineRows(line, previous chatLine) []string {
	styles := c.ctx.Styles()
	who := lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true)

	clock := clockOf(line.at)
	indent := strings.Repeat(" ", gutterWidth+2)
	body := renderBody(line.body, c.bodyWidth())
	picture := c.picture(line)

	rows := make([]string, 0, len(body)+len(picture)+1)
	if previous.who != line.who || clockOf(previous.at) != clock {
		rows = append(rows, styles.Muted.Render(gutter(clock))+"  "+
			who.Render(truncateToWidth(line.who, c.bodyWidth())))
	}

	// A picture leads and its filename sits under it, the way the web puts a
	// caption under the card — the picture is what the message is, and the name of
	// the file is what to call it. With nothing drawn, the filename is the message.
	for _, cells := range picture {
		rows = append(rows, indent+cells)
	}
	for _, text := range body {
		rows = append(rows, indent+text)
	}
	return rows
}

// picture is the cells that stand for the line's image, and nothing while the
// column is narrower than the ones it was placed in: the terminal matches the image
// against those cells, so a cut-down placeholder draws a cut-up picture.
func (c *chatScreen) picture(line chatLine) []string {
	if line.image.Content == "" || line.image.Cols > c.bodyWidth() {
		return nil
	}
	return strings.Split(line.image.Content, "\n")
}

// bodyWidth is the column a message is drawn in, right of the clock.
func (c *chatScreen) bodyWidth() int {
	return max(c.width-gutterWidth-2, 1)
}

// renderBody draws what somebody said. A body is Markdown by the time it gets
// here — richtext.HTMLToMarkdown converted the rich text it arrived as — so it is
// rendered rather than printed: bold reads bold, a quote gets its bar, a list gets
// its bullets, and a URL stays clickable.
//
// The lines come back styled, which is why they are handed on whole. A string
// carrying escape sequences cannot be truncated afterwards, so the width given
// here is the width the rows have.
func renderBody(body string, width int) []string {
	rendered := markdown.Render(body, width)
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

func (c *chatScreen) dayHeading(at, now time.Time) string {
	styles := c.ctx.Styles()
	heading := lipgloss.NewStyle().Foreground(styles.Theme().Foreground).Bold(true)
	return ruledHeading(styles, dayLabel(at, now), heading, c.width, false)
}

// composer is the rule and the rows the reader writes on. It is as tall as what
// is being typed, so the transcript keeps everything the message does not need.
//
// What is in the field is restyled on the way out: the Markdown reads the way it
// will arrive, and its delimiters dim. Only styling is added — every character the
// field drew, the cursor included, stays where it was.
func (c *chatScreen) composer() []string {
	styles := c.ctx.Styles()
	rule := lipgloss.NewStyle().Foreground(styles.Theme().Border).
		Render(strings.Repeat("─", max(c.width, 1)))

	switch {
	case c.writing:
		marker := lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true).Render("› ")
		rows := strings.Split(styleInlineMarkdown(c.compose.View()), "\n")
		for index, row := range rows {
			if index == 0 {
				rows[index] = marker + row
			} else {
				rows[index] = "  " + row
			}
		}
		return append([]string{rule}, rows...)
	case c.sending:
		return []string{rule, styles.Muted.Render("  Sending…")}
	default:
		return []string{rule, styles.Muted.Render("  " +
			fitRow(styles.Muted, "Write a message", styles.Muted.Render(composeKey), max(c.width-2, 1)))}
	}
}

func (c *chatScreen) HelpBindings() []helpBinding {
	if c.writing {
		return []helpBinding{{"enter", "send"}, {"alt+enter", "new line"}, {"esc", "done"}}
	}
	return []helpBinding{
		{"↑↓", "scroll"},
		{composeKey, "write"},
		{refreshKey, "refresh"},
	}
}

// --- Reading and writing ---

// loadChatPage reads one page of the transcript. Basecamp answers newest first
// and pages backwards, so page 1 is the tail of the conversation and a higher
// number is further back.
func loadChatPage(ctx context.Context, app *appctx.App, chatID int64, page int) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return chatPageMsg{page: page, err: err}
		}
		result, err := app.Account().Campfires().
			ListLines(ctx, chatID, &basecamp.CampfireLineListOptions{Page: page})
		if err != nil {
			return chatPageMsg{page: page, err: err}
		}

		lines := make([]chatLine, 0, len(result.Lines))
		for _, line := range result.Lines {
			lines = append(lines, toChatLine(line))
		}
		return chatPageMsg{page: page, lines: lines}
	}
}

// sayInChat posts what was written. The composer is Markdown, so it is converted
// on the way out and sent as the rich text a chat line is — the same conversion
// `basecamp chat post` does, so a message reads the same whichever way it was
// sent.
//
// An @mention is the one piece of Markdown that does not survive this: a mention is
// an SGID the server has to be asked for, and resolveMentions in
// internal/commands is where that lookup lives. Typing "@Rob" here says "@Rob"
// rather than pinging Rob.
func sayInChat(ctx context.Context, app *appctx.App, chatID int64, said string) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return chatSaidMsg{err: err}
		}
		created, err := app.Account().Campfires().CreateLine(ctx, chatID, richtext.MarkdownToHTML(said),
			&basecamp.CreateLineOptions{ContentType: basecamp.LineContentTypeHTML})
		if err != nil {
			return chatSaidMsg{err: err}
		}
		return chatSaidMsg{line: toChatLine(*created)}
	}
}

func toChatLine(line basecamp.CampfireLine) chatLine {
	who := ""
	if line.Creator != nil {
		who = line.Creator.Name
	}
	return chatLine{
		id:       line.ID,
		who:      richtext.SanitizeSingleLine(who),
		body:     chatBody(line),
		at:       line.CreatedAt,
		imageURL: imageAttachment(line),
	}
}

// chatBody is what a line says, as text a terminal can print. Basecamp keeps it
// as rich text, so it comes over as HTML.
func chatBody(line basecamp.CampfireLine) string {
	if line.Content != "" {
		return richtext.SanitizeTerminal(bodyText(line))
	}
	// An upload has no body of its own — the file is the message.
	if len(line.Attachments) > 0 {
		return attachedFiles(line.Attachments)
	}
	return richtext.SanitizeTerminal(line.Title)
}

func bodyText(line basecamp.CampfireLine) string {
	if !richtext.IsHTML(line.Content) {
		return line.Content
	}
	if body := strings.TrimSpace(richtext.HTMLToMarkdown(line.Content)); body != "" {
		return body
	}
	// A line that is only an embed — a tweet, a video — converts to nothing at
	// all. Its title is the address it embedded, which is the closest thing to
	// what the web would have shown.
	return line.Title
}

// attachedFiles names what was uploaded, with the sizes: a terminal cannot show
// the file, so what it says about it has to be worth reading.
func attachedFiles(attachments []basecamp.CampfireLineAttachment) string {
	named := make([]string, 0, len(attachments))
	for _, file := range attachments {
		name := file.Filename
		if name == "" {
			name = file.Title
		}
		if name == "" {
			name = "attachment"
		}
		if file.ByteSize > 0 {
			named = append(named, fmt.Sprintf("📎 %s (%s)", name, output.HumanSize(file.ByteSize)))
		} else {
			named = append(named, "📎 "+name)
		}
	}
	return strings.Join(named, "\n")
}
