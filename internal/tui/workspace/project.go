package workspace

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// How much of the project's own feed the screen shows under the dock. The web
// shows three lines and a link to the rest; a terminal has room for a few more,
// and "View all activity" under them opens the whole thing.
const projectActivityLimit = 5

// toolKinds names each of Basecamp's tools. The dock calls them by their model
// name; this is what a reader calls them.
var toolKinds = map[string]string{
	"message_board": "Message board",
	"todoset":       "To-dos",
	"vault":         "Docs & Files",
	"schedule":      "Calendar",
	"chat":          "Chat",
	"kanban_board":  "Card table",
	"questionnaire": "Automatic check-ins",
	"inbox":         "Email forwards",
}

// tool is one of the things on a project's dock.
type tool struct {
	id   int64
	kind string
	name string

	// position is the order the reader dragged the dock into, which is the order
	// the web's grid reads in.
	position int
}

// label is what the reader called this tool, and what kind of tool it is when
// those are not the same thing. A card table called "HEY CLI" needs saying; a
// chat called "Chat" does not.
func (t tool) label() (name, kind string) {
	known := toolKinds[t.kind]
	if known == "" || strings.EqualFold(known, t.name) {
		return t.name, ""
	}
	return t.name, known
}

// link is one of a project's external links — a repo, a Figma file, a Google
// Doc. Basecamp calls them Doors, and they sit on the same dock as the tools
// with their own run of positions.
type link struct {
	id      int64
	title   string
	url     string
	service string

	// position is where on the dock it was dragged. Doors share one run of
	// positions with the tools, so the numbers are not 1, 2, 3 — only their
	// order matters.
	position int32
}

// projectLoadedMsg is the project with its dock, projectActivityMsg its own
// feed, and projectLinksMsg its external links. Three reads, so the screen fills
// in as they land.
type projectLoadedMsg struct {
	project project
	tools   []tool
	err     error
}

type projectActivityMsg struct {
	entries []activity
	err     error
}

type projectLinksMsg struct {
	links []link
	err   error
}

// projectScreen is a project: what it is called, what it is for, the tools on
// its dock, the links out of it, and what has happened in it lately.
//
// The web draws the dock as a grid of cards, each previewing what is inside it —
// the last few messages, a card table's columns and their counts, the newest
// files. Those previews are one read per tool and one layout per kind; this
// shows the dock as the list it is, and the previews come with the screens
// behind them.
type projectScreen struct {
	ctx     *Context
	project project

	tools    []tool
	links    []link
	activity []activity

	pending int
	notice  string

	cursor int
	offset int
	width  int
	height int

	now func() time.Time
}

func newProject(ctx *Context, open project) *projectScreen {
	return &projectScreen{ctx: ctx, project: open, now: time.Now}
}

func (p *projectScreen) Init() tea.Cmd {
	p.pending = 3
	p.notice = ""
	return tea.Batch(
		loadProject(p.ctx.Ctx(), p.ctx.app, p.project.id),
		loadProjectActivity(p.ctx.Ctx(), p.ctx.app, p.project.id),
		loadProjectLinks(p.ctx.Ctx(), p.ctx.app, p.project.id),
	)
}

func (p *projectScreen) Title() string { return p.project.name }

func (p *projectScreen) Loading() bool { return false }

func (p *projectScreen) Resize(width, height int) {
	p.width = width
	p.height = height
	p.scrollToCursor()
}

func (p *projectScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case projectLoadedMsg:
		p.settle(msg.err, "Could not load the project")
		if msg.err == nil {
			// The name and description come from this read rather than from the
			// row that was clicked: a row can be a page old.
			p.project = msg.project
			p.tools = msg.tools
		}
		p.clampCursor()
		return nil, true

	case projectActivityMsg:
		p.settle(msg.err, "Could not load the activity")
		p.activity = msg.entries
		return nil, true

	case projectLinksMsg:
		p.settle(msg.err, "Could not load the external links")
		p.links = msg.links
		p.clampCursor()
		return nil, true
	}
	return nil, false
}

func (p *projectScreen) settle(err error, what string) {
	p.pending = max(p.pending-1, 0)
	if err != nil && p.notice == "" {
		p.notice = errorNotice(what, err)
	}
}

func (p *projectScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyUp:
		p.cursor = max(p.cursor-1, 0)
	case tea.KeyDown:
		p.cursor = min(p.cursor+1, p.itemCount()-1)
	case tea.KeyEnter:
		return p.open()
	default:
		return nil
	}

	p.scrollToCursor()
	return nil
}

// projectSpot is what the cursor can be standing on.
type projectSpot int

const (
	spotProjectActivity projectSpot = iota
	spotProjectTool
	spotProjectLink
)

// itemCount is how many places the cursor can stand: the dock, then the links,
// then the button that opens the whole feed.
func (p *projectScreen) itemCount() int { return len(p.tools) + len(p.links) + 1 }

func (p *projectScreen) spotAt(index int) (projectSpot, int) {
	switch {
	case index < len(p.tools):
		return spotProjectTool, index
	case index < len(p.tools)+len(p.links):
		return spotProjectLink, index - len(p.tools)
	default:
		return spotProjectActivity, 0
	}
}

// open is what enter does on the cursor's row.
func (p *projectScreen) open() tea.Cmd {
	spot, index := p.spotAt(p.cursor)
	switch spot {
	case spotProjectActivity:
		open := p.project
		return func() tea.Msg { return openProjectActivityMsg{project: open} }
	case spotProjectTool:
		chosen := p.tools[index]
		return func() tea.Msg { return openToolMsg{tool: chosen} }
	case spotProjectLink:
		if index < len(p.links) {
			return p.openLink(p.links[index])
		}
	}
	return nil
}

// openLink leaves the terminal, because that is where the link goes: an external
// link points at a repo or a design file, and there is nothing here to draw.
func (p *projectScreen) openLink(chosen link) tea.Cmd {
	if err := hostutil.OpenBrowser(chosen.url); err != nil {
		return notifyError("Could not open "+chosen.title, err)
	}
	return notify("Opened " + chosen.title + " in your browser")
}

// openToolMsg asks the model for the screen behind one of the dock's tools, and
// openProjectActivityMsg for the project's whole feed.
type openToolMsg struct{ tool tool }

type openProjectActivityMsg struct{ project project }

func (p *projectScreen) clampCursor() {
	p.cursor = max(min(p.cursor, p.itemCount()-1), 0)
	p.scrollToCursor()
}

func (p *projectScreen) scrollToCursor() {
	if p.height <= 0 {
		p.offset = 0
		return
	}

	rows := p.layout()
	at := -1
	for index, row := range rows {
		if row.item == p.cursor {
			at = index
			break
		}
	}
	if at < 0 {
		p.offset = 0
		return
	}

	p.offset = min(p.offset, topOf(rows, at))
	if at >= p.offset+p.height {
		p.offset = at - p.height + 1
	}
	p.offset = max(min(p.offset, max(len(rows)-p.height, 0)), 0)
}

// --- Rendering ---

func (p *projectScreen) View() string {
	rows := p.layout()
	end := min(p.offset+p.height, len(rows))
	lines := make([]string, 0, max(end-p.offset, 0))
	for _, row := range rows[min(p.offset, end):end] {
		lines = append(lines, row.text)
	}
	return strings.Join(lines, "\n")
}

func (p *projectScreen) layout() []homeRow {
	styles := p.ctx.Styles()
	theme := styles.Theme()
	heading := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)

	var rows []homeRow
	plain := func(text string) { rows = append(rows, homeRow{text: text, item: noItem}) }
	item := func(text string, index int) { rows = append(rows, homeRow{text: text, item: index}) }

	if p.notice != "" {
		for _, line := range wrapText(p.notice, p.width) {
			plain(styles.Error.Render(line))
		}
		plain("")
	}

	// The name leads, the way it does on the web: the breadcrumb says where the
	// reader is, and this says what they are looking at. The description sits
	// under it and takes no line of its own when there is none.
	plain(heading.Render(truncateToWidth(p.project.name, max(p.width, 1))))
	if p.project.description != "" {
		plain(styles.Muted.Render(truncateToWidth(p.project.description, max(p.width, 1))))
	}
	plain("")

	loading := p.pending > 0
	index := 0

	plain(ruledHeading(styles, "Tools", heading, p.width, loading))
	for _, on := range p.tools {
		name, kind := on.label()
		item(itemRow{
			label:    name,
			trailing: kind,
			selected: index == p.cursor,
		}.render(styles, p.width), index)
		index++
	}
	if len(p.tools) == 0 && !loading {
		plain(styles.Muted.Render("  Nothing on the dock."))
	}

	// External links get their own section, as they do on the web. They are not
	// tools: every other row on this screen opens a screen, and these leave for
	// the browser.
	if len(p.links) > 0 {
		plain("")
		plain(ruledHeading(styles, "External links", heading, p.width, loading))
		for _, out := range p.links {
			item(itemRow{
				label:    out.title,
				trailing: out.url,
				selected: index == p.cursor,
			}.render(styles, p.width), index)
			index++
		}
	}

	plain("")
	plain(ruledHeading(styles, "Recent activity", heading, p.width, loading))
	now := p.now()
	for _, entry := range p.activity {
		// The feed is read-only here — the screen behind "View all activity" is
		// where its entries can be walked — so no row is ever the selected one.
		for _, line := range activityRows(styles, entry, now, p.width, false) {
			plain(line)
		}
	}
	if len(p.activity) == 0 && !loading {
		plain(styles.Muted.Render("  Nothing yet."))
	}
	item(button{label: "View all activity", selected: index == p.cursor}.render(styles, p.width), index)

	return rows
}

func (p *projectScreen) HelpBindings() []helpBinding {
	return []helpBinding{{"↑↓", "move"}, {"enter", "open"}}
}

// --- Reading ---

// loadProject reads the project and the dock it carries. The dock arrives in no
// particular order with the tools nobody turned on mixed in, so it is sorted by
// the position the reader dragged it into and the disabled ones dropped — which
// is what the web's grid shows.
func loadProject(ctx context.Context, app *appctx.App, projectID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return projectLoadedMsg{err: err}
		}
		found, err := app.Account().Projects().Get(ctx, projectID)
		if err != nil {
			return projectLoadedMsg{err: err}
		}
		return projectLoadedMsg{project: toProject(*found), tools: toTools(found.Dock)}
	}
}

func toTools(dock []basecamp.DockItem) []tool {
	tools := make([]tool, 0, len(dock))
	for _, on := range dock {
		if !on.Enabled {
			continue
		}
		// A tool that is on always has a position. Sorting by zero would put an
		// absent one first, which is a place it has not earned.
		position := 0
		if on.Position != nil {
			position = *on.Position
		}
		tools = append(tools, tool{
			id:       on.ID,
			kind:     on.Name,
			name:     richtext.SanitizeSingleLine(on.Title),
			position: position,
		})
	}

	sort.SliceStable(tools, func(a, b int) bool { return tools[a].position < tools[b].position })
	return tools
}

// loadProjectLinks reads the project's external links.
//
// They come from the recordings list rather than from the project, because the
// project's own dock leaves them out on purpose — api/projects/_project.json
// filters Door out of the tool listing, since a door has no perma to switch to
// the way a tool does. The recordings query is the only endpoint that serves the
// whole door: its title, where it points, and which service it is.
func loadProjectLinks(ctx context.Context, app *appctx.App, projectID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return projectLinksMsg{err: err}
		}
		result, err := app.Account().Recordings().List(ctx, basecamp.RecordingTypeDoor,
			&basecamp.RecordingsListOptions{Bucket: []int64{projectID}})
		if err != nil {
			return projectLinksMsg{err: err}
		}
		return projectLinksMsg{links: toLinks(result.Recordings)}
	}
}

// toLinks puts the doors in the order the reader dragged them into. The
// recordings query answers newest first, which is not an order anybody arranged.
func toLinks(recordings []basecamp.Recording) []link {
	links := make([]link, 0, len(recordings))
	for _, door := range recordings {
		out := link{
			id:       door.ID,
			title:    richtext.SanitizeSingleLine(door.Title),
			url:      door.URL,
			position: door.Position,
		}
		if door.Service != nil {
			out.service = door.Service.Name
		}
		links = append(links, out)
	}

	sort.SliceStable(links, func(a, b int) bool { return links[a].position < links[b].position })
	return links
}

func loadProjectActivity(ctx context.Context, app *appctx.App, projectID int64) tea.Cmd {
	return func() tea.Msg {
		entries, err := projectEvents(ctx, app, projectID,
			&basecamp.TimelineListOptions{Limit: projectActivityLimit, Page: 1})
		if err != nil {
			return projectActivityMsg{err: err}
		}
		return projectActivityMsg{entries: entries}
	}
}

// loadProjectActivityPage is the same read, one page at a time, for the screen
// behind "View all activity".
func loadProjectActivityPage(ctx context.Context, app *appctx.App, projectID int64, page int) tea.Cmd {
	return func() tea.Msg {
		entries, err := projectEvents(ctx, app, projectID, &basecamp.TimelineListOptions{Page: page})
		if err != nil {
			return activityPageMsg{page: page, err: err}
		}
		return activityPageMsg{page: page, events: entries}
	}
}

// projectEvents reads one page of a project's own timeline. Unlike the
// account-wide feed, this one walks back through the whole history.
func projectEvents(ctx context.Context, app *appctx.App, projectID int64,
	opts *basecamp.TimelineListOptions) ([]activity, error) {
	if err := app.RequireAccount(); err != nil {
		return nil, err
	}
	result, err := app.Account().Timeline().ProjectTimeline(ctx, projectID, opts)
	if err != nil {
		return nil, err
	}

	entries := make([]activity, 0, len(result.Events))
	for _, event := range result.Events {
		entries = append(entries, toActivity(event))
	}
	return entries, nil
}
