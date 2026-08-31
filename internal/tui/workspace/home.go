package workspace

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// The keys the web puts on buttons down the left of the home screen. Here
	// they are just keys: a column of buttons in a terminal is a column of
	// things to tab past.
	newProjectKey = "n"
	newFolderKey  = "f"
	invitePeople  = "i"
	adminlandKey  = "a"

	// What the web's right-hand column shows, from the same read.
	homeActivityLimit = 5

	// A folder wears one; a project does not, and gets the same indent so their
	// names line up under each other.
	folderIcon = "📁"
)

// folder is one of the home screen's folders — a Stack, in Basecamp's own terms.
//
// It carries the projects filed inside it, which is both the count on its own
// row and the list the loose projects below are winnowed against.
type folder struct {
	id       int64
	name     string
	color    string
	projects []int64
}

// folderColors maps Basecamp's own color names — the enum on Bucket's user
// customization — onto ANSI slots rather than onto hex.
//
// A named slot is what the terminal's own theme paints, so an Omarchy retint
// carries a folder's color along with everything else. Eleven names into
// sixteen slots means orange and brown land on their nearest bright neighbor;
// white is the default and takes no color at all.
var folderColors = map[string]color.Color{
	"red":    lipgloss.Red,
	"orange": lipgloss.BrightRed,
	"yellow": lipgloss.Yellow,
	"green":  lipgloss.Green,
	"blue":   lipgloss.Blue,
	"aqua":   lipgloss.Cyan,
	"purple": lipgloss.Magenta,
	"pink":   lipgloss.BrightMagenta,
	"gray":   lipgloss.BrightBlack,
	"brown":  lipgloss.BrightYellow,
}

// activity is one entry of the recent-activity feed.
type activity struct {
	who   string
	what  string
	where string
	when  string
}

// naming is which thing the reader is typing a name for, if either.
type naming int

const (
	namingNothing naming = iota
	namingProject
	namingFolder
)

// home is the screen the workspace opens on, and the one esc always comes back
// to: the folders and projects the web's home grid shows, and the recent
// activity its right-hand column shows.
//
// The web's left-hand buttons are keys here instead — n, f, i, a. A terminal has
// no pointer to move to a button, so a button is only a key you cannot press
// until you have found it.
type home struct {
	ctx *Context

	folders  []folder
	projects []project
	activity []activity

	// The projects as read, before the ones filed in a folder are dropped. Kept
	// because the two reads race: whichever lands second does the dropping.
	everyProject []project

	// Each read answers on its own, so the screen fills in as they land rather
	// than waiting on the slowest.
	pending int
	notice  string

	// The name being typed for a new project or folder.
	naming naming
	name   textinput.Model

	cursor int
	offset int
	width  int
	height int
}

func newHome(ctx *Context) *home {
	name := textinput.New()
	name.Prompt = ""

	return &home{ctx: ctx, name: name}
}

func (h *home) Init() tea.Cmd {
	h.pending = 3
	return tea.Batch(
		loadFolders(h.ctx.Ctx(), h.ctx.app),
		loadHomeProjects(h.ctx.Ctx(), h.ctx.app),
		loadActivity(h.ctx.Ctx(), h.ctx.app, time.Now()),
	)
}

func (h *home) Title() string { return "Home" }

func (h *home) Loading() bool { return false }

func (h *home) Resize(width, height int) {
	h.width = width
	h.height = height
	h.name.SetWidth(max(width-2, 1))
	h.scrollToCursor()
}

// CapturingInput is true only while a name is being typed, when every key is
// part of that name rather than a shortcut.
func (h *home) CapturingInput() bool { return h.naming != namingNothing }

// --- Reading ---

// Each read answers with its own message, so the screen fills in as they land.
type homeFoldersMsg struct {
	folders []folder
	err     error
}

type homeProjectsMsg struct {
	projects []project
	err      error
}

type homeActivityMsg struct {
	entries []activity
	err     error
}

// homeMadeMsg is the answer to creating a project or a folder.
type homeMadeMsg struct {
	what string
	name string
	err  error
}

func (h *home) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case homeFoldersMsg:
		h.settle(msg.err, "Could not load the folders")
		h.folders = msg.folders
		h.unfile()
		return nil, true

	case homeProjectsMsg:
		h.settle(msg.err, "Could not load the projects")
		h.everyProject = msg.projects
		h.unfile()
		return nil, true

	case homeActivityMsg:
		h.settle(msg.err, "Could not load the recent activity")
		h.activity = msg.entries
		return nil, true

	case homeMadeMsg:
		if msg.err != nil {
			return notifyError("Could not create "+msg.what, msg.err), true
		}
		// The new one belongs in the list it was made for, which means reading
		// that list again rather than guessing where the server filed it.
		return tea.Batch(notify("Created "+msg.name), h.Init()), true
	}

	// The cursor blink while a name is being typed.
	name, cmd := h.name.Update(msg)
	h.name = name
	return cmd, false
}

// unfile drops the projects that live in a folder from the list below it. They
// are already on screen inside the folder, and the web's grid leaves them out
// of its loose row for the same reason.
func (h *home) unfile() {
	filed := make(map[int64]bool)
	for _, f := range h.folders {
		for _, id := range f.projects {
			filed[id] = true
		}
	}

	h.projects = make([]project, 0, len(h.everyProject))
	for _, p := range h.everyProject {
		if !filed[p.id] {
			h.projects = append(h.projects, p)
		}
	}
	h.clampCursor()
}

// settle marks one of the three reads answered, and keeps the first complaint —
// a later one is the same outage said twice.
func (h *home) settle(err error, what string) {
	h.pending = max(h.pending-1, 0)
	if err != nil && h.notice == "" {
		h.notice = errorNotice(what, err)
	}
}

func loadFolders(ctx context.Context, app *appctx.App) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return homeFoldersMsg{err: err}
		}
		found, err := app.Account().Folders().List(ctx)
		if err != nil {
			return homeFoldersMsg{err: err}
		}

		folders := make([]folder, 0, len(found))
		for _, f := range found {
			folders = append(folders, toFolder(f))
		}
		return homeFoldersMsg{folders: folders}
	}
}

func toFolder(f basecamp.Folder) folder {
	color := ""
	if f.Color != nil {
		color = strings.ToLower(strings.TrimSpace(*f.Color))
	}
	return folder{
		id:       f.ID,
		name:     richtext.SanitizeSingleLine(f.Name),
		color:    color,
		projects: f.BucketIDs,
	}
}

// loadHomeProjects reads one page of projects. The web's grid shows what the
// reader starred and what they visited lately; the API serves neither, so this
// is the first page in the server's own order — most recently created first.
func loadHomeProjects(ctx context.Context, app *appctx.App) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return homeProjectsMsg{err: err}
		}
		result, err := app.Account().Projects().List(ctx, &basecamp.ProjectListOptions{Page: 1})
		if err != nil {
			return homeProjectsMsg{err: err}
		}

		projects := make([]project, 0, len(result.Projects))
		for _, p := range result.Projects {
			projects = append(projects, toProject(p))
		}
		return homeProjectsMsg{projects: projects}
	}
}

// loadActivity reads the same feed the web's home column reads, capped where the
// web caps it.
func loadActivity(ctx context.Context, app *appctx.App, now time.Time) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return homeActivityMsg{err: err}
		}
		result, err := app.Account().Timeline().
			Progress(ctx, &basecamp.TimelineListOptions{Limit: homeActivityLimit, Page: 1})
		if err != nil {
			return homeActivityMsg{err: err}
		}

		entries := make([]activity, 0, len(result.Events))
		for _, event := range result.Events {
			entries = append(entries, toActivity(event, now))
		}
		return homeActivityMsg{entries: entries}
	}
}

func toActivity(event basecamp.TimelineEvent, now time.Time) activity {
	who := ""
	if event.Creator != nil {
		who = event.Creator.Name
	}
	where := ""
	if event.Bucket != nil {
		where = event.Bucket.Name
	}
	when := time.Time{}
	if event.CreatedAt != nil {
		when = *event.CreatedAt
	}

	// Basecamp words the event itself, and words it with the actor's name in it
	// — "Jorge M. commented on …". Putting the creator in front of that says it
	// twice, and putting the action in front too says it three times. The title
	// is the sentence; who and when go quietly underneath, the way they do in
	// the sidebar.
	what := strings.TrimSpace(event.Title)
	if what == "" {
		what = strings.TrimSpace(event.Action)
	}
	if what == "" {
		what = event.Kind
	}
	return activity{
		who:   richtext.SanitizeSingleLine(who),
		what:  richtext.SanitizeSingleLine(what),
		where: richtext.SanitizeSingleLine(where),
		when:  stamp(when, now),
	}
}

// --- Keys ---

func (h *home) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if h.naming != namingNothing {
		return h.handleNamingKey(msg)
	}

	switch msg.String() {
	case newProjectKey:
		return h.startNaming(namingProject, "New project name")
	case newFolderKey:
		return h.startNaming(namingFolder, "New folder name")
	case invitePeople:
		return h.openWeb("account/enrollments/new", "the invite page")
	case adminlandKey:
		return h.openWeb("account", "Adminland")
	}

	switch msg.Key().Code {
	case tea.KeyUp:
		h.moveCursor(-1)
	case tea.KeyDown:
		h.moveCursor(1)
	case tea.KeyEnter:
		return h.open()
	}
	return nil
}

func (h *home) startNaming(kind naming, placeholder string) tea.Cmd {
	h.naming = kind
	h.name.SetValue("")
	h.name.Placeholder = placeholder
	return h.name.Focus()
}

func (h *home) stopNaming() {
	h.naming = namingNothing
	h.name.SetValue("")
	h.name.Blur()
}

func (h *home) handleNamingKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyEscape:
		h.stopNaming()
		return nil
	case tea.KeyEnter:
		name := strings.TrimSpace(h.name.Value())
		kind := h.naming
		h.stopNaming()
		if name == "" {
			return nil
		}
		if kind == namingFolder {
			return createFolder(h.ctx.Ctx(), h.ctx.app, name)
		}
		return createProject(h.ctx.Ctx(), h.ctx.app, name)
	}

	name, cmd := h.name.Update(msg)
	h.name = name
	return cmd
}

func createProject(ctx context.Context, app *appctx.App, name string) tea.Cmd {
	return func() tea.Msg {
		_, err := app.Account().Projects().Create(ctx, &basecamp.CreateProjectRequest{Name: name})
		return homeMadeMsg{what: "the project", name: name, err: err}
	}
}

func createFolder(ctx context.Context, app *appctx.App, name string) tea.Cmd {
	return func() tea.Msg {
		_, err := app.Account().Folders().Create(ctx, basecamp.CreateFolderRequest{Name: name})
		return homeMadeMsg{what: "the folder", name: name, err: err}
	}
}

// openWeb sends the browser to a page under the account. Adminland and the
// invite flow are web pages with no API behind them, so reaching them means
// leaving the terminal.
//
// The address comes from a project's own app_url rather than from config, which
// holds the API host: an account is only reachable at the host the server named.
func (h *home) openWeb(path, what string) tea.Cmd {
	root := h.accountRoot()
	if root == "" {
		return notify("Cannot open " + what + " until the projects have loaded")
	}
	if err := hostutil.OpenBrowser(root + "/" + path); err != nil {
		return notifyError("Could not open "+what, err)
	}
	return notify("Opened " + what + " in your browser")
}

// accountRoot is the account's own address on the web — everything up to and
// including the account id in a project's app_url.
func (h *home) accountRoot() string {
	for _, p := range h.projects {
		if root, _, found := strings.Cut(p.appURL, "/projects/"); found {
			return root
		}
	}
	return ""
}

// --- The cursor ---

// The cursor walks the screen top to bottom: the button under the activity feed,
// then the folders and projects in one list, then the button under those. The
// feed's own rows are not among them — there is no screen for one entry yet.
type homeSpot int

const (
	spotAllActivity homeSpot = iota
	spotFolder
	spotProject
	spotAllProjects
)

// itemCount is how many places the cursor can stand.
func (h home) itemCount() int { return 1 + len(h.folders) + len(h.projects) + 1 }

// spotAt says what the cursor is standing on, and which folder or project.
func (h home) spotAt(index int) (homeSpot, int) {
	switch {
	case index == 0:
		return spotAllActivity, 0
	case index <= len(h.folders):
		return spotFolder, index - 1
	case index <= len(h.folders)+len(h.projects):
		return spotProject, index - 1 - len(h.folders)
	default:
		return spotAllProjects, 0
	}
}

func (h *home) moveCursor(by int) {
	h.cursor = max(min(h.cursor+by, h.itemCount()-1), 0)
	h.scrollToCursor()
}

func (h *home) clampCursor() {
	h.cursor = max(min(h.cursor, h.itemCount()-1), 0)
	h.scrollToCursor()
}

func (h *home) scrollToCursor() {
	if h.height <= 0 {
		h.offset = 0
		return
	}

	rows := h.layout()
	at := -1
	for index, row := range rows {
		if row.item == h.cursor {
			at = index
			break
		}
	}
	if at < 0 {
		h.offset = 0
		return
	}

	h.offset = min(h.offset, topOf(rows, at))
	if at >= h.offset+h.height {
		h.offset = at - h.height + 1
	}
	h.offset = max(min(h.offset, max(len(rows)-h.height, 0)), 0)
}

// openFolderMsg and openProjectMsg ask the model to open what the cursor is on.
// The screen cannot push onto the stack itself — that belongs to the model.
type openFolderMsg struct{ folder folder }

type openProjectMsg struct{ project project }

// openAllMsg asks the model for one of the two screens the buttons lead to.
type openAllMsg struct{ kind listKind }

// topOf is the row to scroll to when the cursor lands on the one at `at`: the
// run of headings and blank lines directly above it comes with it.
//
// Without this the first thing the cursor can stand on pins the window below
// everything above it — the whole activity feed sits over the button that opens
// it, so scrolling back up to that button would leave the feed off the top of
// the screen for good.
func topOf(rows []homeRow, at int) int {
	top := at
	for top > 0 && rows[top-1].item == noItem {
		top--
	}
	return top
}

// open is what enter does on the cursor's row.
func (h home) open() tea.Cmd {
	spot, index := h.spotAt(h.cursor)
	switch spot {
	case spotAllActivity:
		return func() tea.Msg { return openAllMsg{kind: listActivity} }
	case spotAllProjects:
		return func() tea.Msg { return openAllMsg{kind: listProjects} }
	case spotFolder:
		chosen := h.folders[index]
		return func() tea.Msg { return openFolderMsg{folder: chosen} }
	case spotProject:
		chosen := h.projects[index]
		return func() tea.Msg { return openProjectMsg{project: chosen} }
	}
	return nil
}

// --- Rendering ---

// homeRow is one drawn line and which item it belongs to, so scrolling knows
// where the cursor is. A heading, a gap or a feed line belongs to none.
type homeRow struct {
	text string
	item int
}

func (h home) View() string {
	rows := h.layout()

	end := min(h.offset+h.height, len(rows))
	lines := make([]string, 0, max(end-h.offset, 0))
	for _, row := range rows[min(h.offset, end):end] {
		lines = append(lines, row.text)
	}
	return strings.Join(lines, "\n")
}

func (h home) layout() []homeRow {
	styles := h.ctx.Styles()
	theme := styles.Theme()
	heading := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)

	rows := make([]homeRow, 0, 32)
	plain := func(text string) { rows = append(rows, homeRow{text: text, item: noItem}) }
	item := func(text string, index int) { rows = append(rows, homeRow{text: text, item: index}) }

	if h.naming != namingNothing {
		plain(h.name.View())
		plain("")
	}
	if h.notice != "" {
		for _, line := range wrapText(h.notice, h.width) {
			plain(styles.Error.Render(line))
		}
		plain("")
	}

	loading := h.pending > 0
	index := 0

	// The activity comes first, the way the web's own column reads top-down, and
	// the button under it goes where a reader who has run out of entries is
	// already looking.
	plain(ruledHeading(styles, "Recent activity", heading, h.width, loading))
	for _, entry := range h.activity {
		for _, line := range h.activityRows(entry) {
			plain(line)
		}
	}
	item(h.buttonRow("View all", index), index)
	index++

	plain("")
	plain(ruledHeading(styles, "Projects", heading, h.width, loading))

	// Folders and projects are one list, as they are on the web's card grid: a
	// folder is a project you have not opened yet.
	for _, f := range h.folders {
		rowsFor := h.tintedItemRows(folderIcon, h.folderColor(f), f.name,
			fmt.Sprintf("%d projects", len(f.projects)), index)
		for _, line := range rowsFor {
			item(line, index)
		}
		index++
	}
	for _, p := range h.projects {
		for _, line := range h.itemRows("", p.name, p.description, index) {
			item(line, index)
		}
		index++
	}

	item(h.buttonRow("See all projects", index), index)
	return rows
}

// itemRows is one row of the list: its name, and the quieter line under it. A
// project with no description still gets that line, so every row is the same
// height and the column reads as a list rather than a paragraph.
func (h home) itemRows(icon, label, subtitle string, index int) []string {
	return h.tintedItemRows(icon, nil, label, subtitle, index)
}

// tintedItemRows is itemRows with a color of the reader's own choosing on it,
// which is what a folder has and nothing else does.
//
// The color goes on the name rather than on the icon. A folder's icon is an
// emoji, and an emoji carries its own colors: the terminal paints it from the
// font and ignores the foreground it was handed, so a color put there is a
// color nobody ever sees. It is the card the web colors anyway, not the glyph
// on it.
func (h home) tintedItemRows(icon string, tint color.Color, label, subtitle string, index int) []string {
	theme := h.ctx.Styles().Theme()

	marker := "  "
	style := lipgloss.NewStyle().Foreground(theme.Foreground)
	if tint != nil {
		style = style.Foreground(tint)
	}
	// The cursor wins the row it is on: where the reader is standing has to read
	// at a glance, and a folder's color is not that.
	if index == h.cursor {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		style = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	// A project has no icon and takes the space one would, so the names line up
	// with the folders'. The line underneath is always blank there: an icon
	// repeated under itself reads as a second row rather than the same one.
	blank := strings.Repeat(" ", iconColumns)
	badge := blank
	if icon != "" {
		badge = icon + strings.Repeat(" ", max(iconColumns-tui.DisplayWidth(icon), 1))
	}

	inner := max(h.width-2-iconColumns, 1)
	return []string{
		marker + badge + style.Render(truncateToWidth(label, inner)),
		"  " + blank + h.ctx.Styles().Muted.Render(truncateToWidth(subtitle, inner)),
	}
}

// folderColor is the color the reader gave a folder, or nil — white is
// Basecamp's default and means uncolored, and a palette with no colors in it
// paints nothing either way.
func (h home) folderColor(f folder) color.Color {
	if _, colorless := h.ctx.Styles().Theme().Primary.(lipgloss.NoColor); colorless {
		return nil
	}
	if slot, ok := folderColors[f.color]; ok {
		return slot
	}
	return nil
}

// iconColumns is the room the folder icon takes, plus the space after it. An
// emoji is two cells wide wherever the terminal was asked — see CalibrateWidths.
const iconColumns = 3

// activityRows is one entry of the feed: who did what, then when and where.
// The feed is read-only, so its rows carry no cursor.
func (h home) activityRows(entry activity) []string {
	styles := h.ctx.Styles()
	inner := max(h.width-2, 1)

	rows := []string{"  " + lipgloss.NewStyle().
		Foreground(styles.Theme().Foreground).
		Render(truncateToWidth(entry.what, inner))}

	if meta := strings.Join(nonEmpty(entry.when, entry.who, entry.where), " · "); meta != "" {
		rows = append(rows, "  "+styles.Muted.Render(truncateToWidth(meta, inner)))
	}
	return rows
}

// buttonRow is one of the two rows that lead to a screen of their own.
func (h home) buttonRow(label string, index int) string {
	style := h.ctx.Styles().Muted
	marker := "  "
	if index == h.cursor {
		style = lipgloss.NewStyle().Foreground(h.ctx.Styles().Theme().Primary).Bold(true)
		marker = style.Render("› ")
	}
	return marker + style.Render(truncateToWidth(label+" →", max(h.width-2, 1)))
}

func (h home) HelpBindings() []helpBinding {
	if h.naming != namingNothing {
		return []helpBinding{{"enter", "create"}, {"esc", "cancel"}}
	}
	return []helpBinding{
		{"↑↓", "move"},
		{"enter", "open"},
		{newProjectKey, "new project"},
		{newFolderKey, "new folder"},
		{invitePeople, "invite people"},
		{adminlandKey, "adminland"},
	}
}
