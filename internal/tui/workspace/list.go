package workspace

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// listKind says which list a page belongs to, so an answer that arrives after
// the reader has left one screen for the other is dropped rather than appended
// to whatever is on screen now.
type listKind int

const (
	listProjects listKind = iota
	listActivity
)

// listItem is one row: a line and the quieter line under it.
type listItem struct {
	label    string
	subtitle string
	project  *project
}

// listPageMsg is a page of whatever a list screen is listing.
type listPageMsg struct {
	kind  listKind
	page  int
	items []listItem
	err   error
}

// listScreen is a screen that is one long list, read a page at a time: all the
// projects, all the recent activity. What it lists comes from the caller; the
// walking, the scrolling and the paging are the same either way.
type listScreen struct {
	ctx   *Context
	kind  listKind
	title string

	items  []listItem
	page   int
	paging bool
	done   bool
	notice string

	cursor int
	offset int
	width  int
	height int
}

func newAllProjects(ctx *Context) *listScreen {
	return &listScreen{ctx: ctx, kind: listProjects, title: "All projects"}
}

func (l *listScreen) Init() tea.Cmd {
	l.items, l.page, l.done, l.notice = nil, 0, false, ""
	return l.readMore()
}

func (l *listScreen) Title() string { return l.title }

func (l *listScreen) Loading() bool { return false }

func (l *listScreen) Resize(width, height int) {
	l.width = width
	l.height = height
	l.scrollToCursor()
}

func (l *listScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	page, ok := msg.(listPageMsg)
	if !ok || page.kind != l.kind {
		return nil, false
	}

	l.paging = false
	if page.err != nil {
		// The rows already on screen are still good, so a page that failed to
		// arrive stops the walk rather than replacing them.
		l.done = true
		if len(l.items) == 0 {
			l.notice = errorNotice("Could not load "+strings.ToLower(l.title), page.err)
		}
		return nil, true
	}
	if len(page.items) == 0 {
		l.done = true
		return nil, true
	}

	l.page = page.page
	l.items = append(l.items, page.items...)
	l.scrollToCursor()
	return nil, true
}

func (l *listScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyUp:
		l.cursor = max(l.cursor-1, 0)
	case tea.KeyDown:
		l.cursor = min(l.cursor+1, max(len(l.items)-1, 0))
	case tea.KeyEnter:
		return l.open()
	default:
		return nil
	}

	l.scrollToCursor()
	return l.readMore()
}

// open is what enter does, for a list whose rows lead somewhere. The activity
// feed's do not — there is no screen for one yet.
func (l *listScreen) open() tea.Cmd {
	if l.cursor >= len(l.items) {
		return nil
	}
	chosen := l.items[l.cursor].project
	if chosen == nil {
		return nil
	}
	return func() tea.Msg { return openProjectMsg{project: *chosen} }
}

// readMore asks for the page below what is loaded, when the cursor has come near
// the end of it or there is not enough to fill the screen.
func (l *listScreen) readMore() tea.Cmd {
	if l.paging || l.done {
		return nil
	}
	if len(l.items) > 0 && len(l.items) > l.height && l.cursor < len(l.items)-pageAheadBy {
		return nil
	}

	l.paging = true
	return loadAllProjects(l.ctx.Ctx(), l.ctx.app, l.page+1)
}

func (l *listScreen) scrollToCursor() {
	if l.height <= 0 {
		l.offset = 0
		return
	}
	rows := l.height / rowsPerListItem

	l.offset = min(l.offset, l.cursor)
	if l.cursor >= l.offset+rows {
		l.offset = l.cursor - rows + 1
	}
	l.offset = max(min(l.offset, max(len(l.items)-rows, 0)), 0)
}

// Every row is its label and the quieter line under it, so a screen this tall
// holds half as many rows as it has lines.
const rowsPerListItem = 2

func (l *listScreen) View() string {
	styles := l.ctx.Styles()

	switch {
	case l.notice != "":
		return strings.Join(wrapText(l.notice, l.width), "\n")
	case len(l.items) == 0 && l.paging:
		return styles.Muted.Render("Loading…")
	case len(l.items) == 0:
		return styles.Muted.Render("Nothing here.")
	}

	end := min(l.offset+l.height/rowsPerListItem, len(l.items))
	lines := make([]string, 0, l.height)
	for index := l.offset; index < end; index++ {
		lines = append(lines, listRows(styles, l.items[index], index == l.cursor, l.width)...)
	}
	return strings.Join(lines, "\n")
}

func (l *listScreen) HelpBindings() []helpBinding {
	return []helpBinding{{"↑↓", "move"}, {"enter", "open"}}
}

// listRows draws one item: its label, and the quieter line under it. The second
// line is drawn even when it is empty, so every row is the same height and the
// list reads as a column rather than a paragraph.
func listRows(styles *tui.Styles, item listItem, selected bool, width int) []string {
	theme := styles.Theme()
	inner := max(width-2, 1)

	marker := "  "
	label := lipgloss.NewStyle().Foreground(theme.Foreground)
	if selected {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		label = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}
	return []string{
		marker + label.Render(truncateToWidth(item.label, inner)),
		"  " + styles.Muted.Render(truncateToWidth(item.subtitle, inner)),
	}
}

// --- Reading ---

func loadAllProjects(ctx context.Context, app *appctx.App, page int) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return listPageMsg{kind: listProjects, page: page, err: err}
		}
		result, err := app.Account().Projects().List(ctx, &basecamp.ProjectListOptions{Page: page})
		if err != nil {
			return listPageMsg{kind: listProjects, page: page, err: err}
		}

		items := make([]listItem, 0, len(result.Projects))
		for _, p := range result.Projects {
			found := toProject(p)
			items = append(items, listItem{
				label:    found.name,
				subtitle: found.description,
				project:  &found,
			})
		}
		return listPageMsg{kind: listProjects, page: page, items: items}
	}
}

func toProject(p basecamp.Project) project {
	return project{
		id:          p.ID,
		name:        richtext.SanitizeSingleLine(p.Name),
		description: richtext.SanitizeSingleLine(p.Description),
		appURL:      p.AppURL,
	}
}
