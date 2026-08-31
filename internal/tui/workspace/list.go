package workspace

import (
	"context"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

const (
	// The key that shows the archived and trashed projects alongside the active
	// ones, which is the web's "Show Archived and Trashed" switch.
	inactiveKey = "a"

	// The width of the letter column down the left, which the web puts its
	// group letters in.
	initialWidth = 4
)

// listKind says which of the two "see all" screens a button leads to.
type listKind int

const (
	listProjects listKind = iota
	listActivity
)

// directoryLoadedMsg is the whole directory, in one answer.
type directoryLoadedMsg struct {
	projects []project
	// inactive is whether this read included the archived and trashed ones, so
	// an answer to the previous state of the switch is dropped rather than shown
	// under the new one.
	inactive bool
	err      error
}

// listScreen is the whole project directory: every project the reader can see,
// in one alphabetical list broken by initial letter.
//
// It reads everything rather than a page at a time. A list sorted a page at a
// time is sorted only against itself — B follows Z as soon as the second page
// lands — and there is no ordering the server will do for us: projects.json
// answers newest-created first. So the sort has to happen here, and sorting here
// means having all of it.
type listScreen struct {
	ctx *Context

	// inside is the folder being looked into, and nil for the whole account.
	// A folder holds active projects and nothing else, so it has no switch.
	inside *folder

	projects []project
	loading  bool
	notice   string

	// inactive is whether the archived and trashed ones are showing.
	inactive bool

	// The quick-find box, and whether the reader is typing in it.
	find    textinput.Model
	finding bool

	cursor int
	offset int
	width  int
	height int
}

func newAllProjects(ctx *Context) *listScreen {
	return &listScreen{ctx: ctx, find: newFindField()}
}

// newFolder is the projects filed into one folder, which reads the same way the
// whole directory does — the same rows, the same find field, the same order.
func newFolder(ctx *Context, inside folder) *listScreen {
	return &listScreen{ctx: ctx, inside: &inside, find: newFindField()}
}

func newFindField() textinput.Model {
	find := textinput.New()
	find.Prompt = ""
	find.Placeholder = "Find a project…"
	return find
}

func (l *listScreen) Init() tea.Cmd {
	l.projects, l.notice = nil, ""
	l.loading = true
	if l.inside != nil {
		return loadFolder(l.ctx.Ctx(), l.ctx.app, l.inside.id)
	}
	return loadDirectory(l.ctx.Ctx(), l.ctx.app, l.inactive)
}

func (l *listScreen) Title() string {
	if l.inside != nil {
		return l.inside.name
	}
	return "All projects"
}

func (l *listScreen) Loading() bool { return l.loading }

func (l *listScreen) Resize(width, height int) {
	l.width = width
	l.height = height
	l.find.SetWidth(max(width-2, 1))
	l.scrollToCursor()
}

// CapturingInput is true while the find box has the keys.
func (l *listScreen) CapturingInput() bool { return l.finding }

// HandleBack closes the find box rather than letting esc pop the screen.
func (l *listScreen) HandleBack() bool {
	if !l.finding && l.find.Value() == "" {
		return false
	}
	l.clearFind()
	return true
}

func (l *listScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	loaded, ok := msg.(directoryLoadedMsg)
	if !ok {
		if l.finding {
			find, cmd := l.find.Update(msg)
			l.find = find
			return cmd, false
		}
		return nil, false
	}
	if loaded.inactive != l.inactive {
		return nil, true
	}

	l.loading = false
	if loaded.err != nil {
		l.notice = errorNotice("Could not load the projects", loaded.err)
		return nil, true
	}

	// Sorted here rather than in the read, so every source lands in the same
	// order whether or not it remembered to.
	l.projects = loaded.projects
	sortProjects(l.projects)
	l.clampCursor()
	return nil, true
}

func (l *listScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if l.finding {
		return l.handleFindKey(msg)
	}

	switch {
	case msg.String() == findKey:
		l.finding = true
		return l.find.Focus()
	case msg.String() == inactiveKey && l.inside == nil:
		return l.toggleInactive()
	case msg.String() == editFolderKey && l.inside != nil:
		edited := *l.inside
		return func() tea.Msg { return editFolderMsg{folder: edited} }
	case msg.Key().Code == tea.KeyUp:
		l.cursor = max(l.cursor-1, 0)
	case msg.Key().Code == tea.KeyDown:
		l.cursor = min(l.cursor+1, max(l.visible()-1, 0))
	case msg.Key().Code == tea.KeyEnter:
		return l.open()
	default:
		return nil
	}

	l.scrollToCursor()
	return nil
}

// toggleInactive flips the archived and trashed ones in and out. They come from
// their own reads, so the whole directory is read again rather than filtered.
func (l *listScreen) toggleInactive() tea.Cmd {
	l.inactive = !l.inactive
	l.loading = true
	l.cursor = 0
	return loadDirectory(l.ctx.Ctx(), l.ctx.app, l.inactive)
}

func (l *listScreen) handleFindKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyEnter:
		l.finding = false
		l.find.Blur()
		return nil
	case tea.KeyEsc:
		l.clearFind()
		return nil
	}

	find, cmd := l.find.Update(msg)
	l.find = find
	l.clampCursor()
	return cmd
}

func (l *listScreen) clearFind() {
	l.finding = false
	l.find.Blur()
	l.find.SetValue("")
	l.clampCursor()
}

// open goes to the project the cursor is on.
func (l *listScreen) open() tea.Cmd {
	showing := l.showing()
	if l.cursor >= len(showing) {
		return nil
	}
	chosen := showing[l.cursor]
	return func() tea.Msg { return openProjectMsg{project: chosen} }
}

// --- What is on screen ---

// showing is the projects the find box leaves, sorted and ready to draw.
func (l *listScreen) showing() []project {
	needle := strings.ToLower(strings.TrimSpace(l.find.Value()))
	if needle == "" {
		return l.projects
	}

	kept := make([]project, 0, len(l.projects))
	for _, found := range l.projects {
		if strings.Contains(strings.ToLower(found.name), needle) ||
			strings.Contains(strings.ToLower(found.description), needle) {
			kept = append(kept, found)
		}
	}
	return kept
}

func (l *listScreen) visible() int { return len(l.showing()) }

func (l *listScreen) clampCursor() {
	l.cursor = max(min(l.cursor, l.visible()-1), 0)
	l.scrollToCursor()
}

func (l *listScreen) scrollToCursor() {
	if l.height <= 0 {
		l.offset = 0
		return
	}

	rows := l.layout()
	at := -1
	for index, row := range rows {
		if row.item == l.cursor {
			at = index
			break
		}
	}
	if at < 0 {
		l.offset = 0
		return
	}

	// Scrolling up to a project brings its letter back with it.
	l.offset = min(l.offset, topOf(rows, at))
	if at >= l.offset+l.height {
		l.offset = at - l.height + 1
	}
	l.offset = max(min(l.offset, max(len(rows)-l.height, 0)), 0)
}

// --- Rendering ---

func (l *listScreen) View() string {
	if l.notice != "" {
		return strings.Join(wrapText(l.notice, l.width), "\n")
	}

	rows := l.layout()
	end := min(l.offset+l.height, len(rows))
	lines := make([]string, 0, max(end-l.offset, 0))
	for _, row := range rows[min(l.offset, end):end] {
		lines = append(lines, row.text)
	}
	return strings.Join(lines, "\n")
}

func (l *listScreen) layout() []homeRow {
	styles := l.ctx.Styles()
	showing := l.showing()

	var rows []homeRow
	plain := func(text string) { rows = append(rows, homeRow{text: text, item: noItem}) }
	item := func(text string, at int) { rows = append(rows, homeRow{text: text, item: at}) }

	plain(l.findRow())
	plain(l.findRule())
	if l.inside == nil {
		plain(l.switchRow())
	}
	plain("")

	switch {
	case l.loading:
		plain(styles.Muted.Render("Loading…"))
		return rows
	case len(l.projects) == 0 && l.inside != nil:
		plain(styles.Muted.Render("This folder is empty."))
		return rows
	case len(l.projects) == 0:
		plain(styles.Muted.Render("No projects."))
		return rows
	case len(showing) == 0:
		plain(styles.Muted.Render("Nothing matches " + strings.TrimSpace(l.find.Value()) + "."))
		return rows
	}

	lettered := len(showing) > minRowsForLetters
	initial := ""
	for index, found := range showing {
		if at := newSortName(found.name).initial(); lettered && at != initial {
			initial = at
			if index > 0 {
				plain("")
			}
			plain(l.initialHeading(initial))
		}
		item(itemRow{
			label:    found.name,
			trailing: l.trailingFor(found),
			indent:   initialWidth,
			selected: index == l.cursor,
		}.render(styles, l.width), index)
	}
	return rows
}

// trailingFor is the quieter half of a project's row: what it is for, and what
// state it is in when that is not the ordinary one.
func (l *listScreen) trailingFor(found project) string {
	if found.status != "" && found.status != "active" {
		return strings.Join(nonEmpty(found.status, found.description), " · ")
	}
	return found.description
}

// minRowsForLetters is how long a list has to be before the letter separators
// are worth their space. They are an index — something to skim a hundred names
// with — and a folder holding four is quicker to read without them.
const minRowsForLetters = 20

// findRow is the find box, laid out the way the jump menu's is: the field's own
// name on the left and the key that reaches it on the right, until somebody is
// typing in it.
func (l *listScreen) findRow() string {
	styles := l.ctx.Styles()
	inner := max(l.width-2, 1)

	switch needle := strings.TrimSpace(l.find.Value()); {
	case l.finding:
		return l.find.View()
	case needle == "":
		return fitRow(styles.Muted, "Find a project", styles.Muted.Render(findKey), inner)
	default:
		return fitRow(lipgloss.NewStyle().Foreground(styles.Theme().Primary),
			needle, styles.Muted.Render("esc"), inner)
	}
}

// findRule is the line under the find field. The web draws a box around its
// input; a terminal only needs the bottom of one to say "you type here".
func (l *listScreen) findRule() string {
	theme := l.ctx.Styles().Theme()
	edge := theme.Border
	if l.finding {
		edge = theme.Primary
	}
	return lipgloss.NewStyle().Foreground(edge).Render(strings.Repeat("─", max(l.width, 1)))
}

// switchRow is the web's "Show Archived and Trashed" switch. A terminal has no
// pointer to flick it with, so the key that flicks it sits where the finger
// would go.
func (l *listScreen) switchRow() string {
	styles := l.ctx.Styles()
	return "  " + l.toggle() + " " +
		styles.Muted.Render("Show archived and trashed") + "  " +
		styles.Muted.Render(inactiveKey)
}

// toggle draws the switch itself: a track with the knob at one end of it, lit
// when it is on and dim when it is off. Two half-block glyphs make the track,
// which reads as a switch at any font size and needs no box drawing.
func (l *listScreen) toggle() string {
	theme := l.ctx.Styles().Theme()

	track := lipgloss.NewStyle().Foreground(theme.Border)
	knob := lipgloss.NewStyle().Foreground(theme.Muted)
	if l.inactive {
		track = lipgloss.NewStyle().Foreground(theme.Primary)
		knob = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	if l.inactive {
		return track.Render("━") + knob.Render("⬤")
	}
	return knob.Render("⬤") + track.Render("━")
}

func (l *listScreen) initialHeading(initial string) string {
	styles := l.ctx.Styles()
	heading := lipgloss.NewStyle().Foreground(styles.Theme().Primary).Bold(true)
	return ruledHeading(styles, initial, heading, l.width, false)
}

func (l *listScreen) HelpBindings() []helpBinding {
	if l.finding {
		return []helpBinding{{"enter", "apply"}, {"esc", "clear"}}
	}

	bindings := []helpBinding{{"↑↓", "move"}, {"enter", "open"}, {findKey, "find"}}
	if l.inside != nil {
		return append(bindings, helpBinding{editFolderKey, "edit folder"})
	}
	return append(bindings, helpBinding{inactiveKey, "archived"})
}

// --- Reading ---

// loadDirectory reads the whole directory. Page 0 walks the entire Link rel="next"
// chain, which is what an alphabetical list needs: sorting one page at a time
// sorts it only against itself.
//
// Archived and trashed come from their own reads. The endpoint takes one status
// at a time, so showing all three means asking three times and merging.
//
// The active read sends no status at all. Basecamp accepts only archived and
// trashed — ProjectsController#status_param_for_api runs the parameter through
// presence_in(%w[ archived trashed ]) and answers 400 for anything else present,
// "active" included. The SDK's ProjectStatusActive is a constant the server
// rejects; active is the default, and the way to ask for it is to say nothing.
func loadDirectory(ctx context.Context, app *appctx.App, inactive bool) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return directoryLoadedMsg{inactive: inactive, err: err}
		}

		statuses := []basecamp.ProjectStatus{""}
		if inactive {
			statuses = append(statuses,
				basecamp.ProjectStatusArchived, basecamp.ProjectStatusTrashed)
		}

		var projects []project
		for _, status := range statuses {
			result, err := app.Account().Projects().
				List(ctx, &basecamp.ProjectListOptions{Status: status})
			if err != nil {
				return directoryLoadedMsg{inactive: inactive, err: err}
			}
			for _, p := range result.Projects {
				projects = append(projects, toProject(p))
			}
		}

		return directoryLoadedMsg{inactive: inactive, projects: projects}
	}
}

// loadFolder reads one folder with the projects filed into it expanded, which is
// one request rather than one per project.
func loadFolder(ctx context.Context, app *appctx.App, folderID int64) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return directoryLoadedMsg{err: err}
		}
		found, err := app.Account().Folders().Get(ctx, folderID)
		if err != nil {
			return directoryLoadedMsg{err: err}
		}

		projects := make([]project, 0, len(found.Projects))
		for _, p := range found.Projects {
			projects = append(projects, toProject(p))
		}
		return directoryLoadedMsg{projects: projects}
	}
}

// sortProjects puts the directory in Basecamp's own order, which is not the
// order a plain string compare would give. See sortName.
func sortProjects(projects []project) {
	keys := make(map[int64]sortName, len(projects))
	for _, found := range projects {
		keys[found.id] = newSortName(found.name)
	}
	sort.SliceStable(projects, func(a, b int) bool {
		return keys[projects[a].id].before(keys[projects[b].id])
	})
}

func toProject(p basecamp.Project) project {
	return project{
		id:          p.ID,
		name:        richtext.SanitizeSingleLine(p.Name),
		description: richtext.SanitizeSingleLine(p.Description),
		status:      p.Status,
		appURL:      p.AppURL,
	}
}
