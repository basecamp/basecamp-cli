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
	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// The keys that open the menu, and the words the top line says about them.
	menuKey      = "ctrl+j"
	menuAltKey   = "ctrl+k"
	menuHintText = "ctrl+j/k for menu"

	// searchKey puts the cursor in the menu's search field, wherever in the menu
	// the reader is.
	searchKey = "/"

	// The menu takes three fifths of the screen, down to a floor where the
	// labels still have room to sit next to their keys.
	menuWidthNumerator   = 3
	menuWidthDenominator = 5
	menuMinWidth         = 28

	// The row the menu hangs from: one below the top line, which leaves the
	// account and its caret showing above it.
	menuTopRow = 1

	// Two border rows, the search field, and the rule under it.
	menuChrome = 4
)

// menuEntry is one thing the menu can take you to: a place with a number, or a
// project with a name.
type menuEntry struct {
	key   string
	label string
	hint  string
	open  func(m *model) tea.Cmd
}

// menuSection groups the entries under a heading, the way the web's jump menu
// groups them.
type menuSection struct {
	title   string
	entries []menuEntry
}

// menu is the panel behind the wordmark's chevron: everywhere the workspace can
// take you, with a search field over it.
//
// It belongs to the model rather than to a screen: it opens over whatever the
// reader is looking at, gives it straight back, and is the same menu wherever
// they are.
type menu struct {
	styles *tui.Styles

	open      bool
	searching bool
	search    textinput.Model

	// The walk down the projects: what has arrived, the page it came from,
	// whether one is in flight, and whether the server has run out of them.
	projects       []project
	projectsPage   int
	projectsLoaded bool
	projectsPaging bool
	projectsDone   bool
	projectsNotice string

	cursor int
	offset int
	width  int
	height int
}

// project is a project as the menu lists it.
type project struct {
	id   int64
	name string
}

// projectsLoadedMsg is one page of projects, the first or a later one.
type projectsLoadedMsg struct {
	page     int
	projects []project
	err      error
}

// appendProjects puts a page under what has arrived. A page with nothing in it
// is the end of the list.
func (n *menu) appendProjects(msg projectsLoadedMsg) {
	n.projectsPaging = false
	n.projectsLoaded = true
	n.projectsNotice = ""

	if len(msg.projects) == 0 {
		n.projectsDone = true
		return
	}
	n.projectsPage = msg.page
	n.projects = append(n.projects, msg.projects...)
	n.clampCursor()
}

// wantsMoreProjects is whether the next page is worth asking for. Two reasons to
// want one: the cursor has come near the end of what is loaded, or there are not
// enough entries to fill the box — which is also what pulls the rest in behind a
// search, since a narrow query leaves few matches on screen.
func (n menu) wantsMoreProjects() bool {
	if !n.open || n.projectsPaging || n.projectsDone {
		return false
	}
	if !n.projectsLoaded {
		return true
	}
	return n.count() < n.visibleRows() || n.cursor >= n.count()-pageAheadBy
}

func newMenu(styles *tui.Styles) menu {
	search := textinput.New()
	search.Placeholder = "Search"
	search.Prompt = ""

	return menu{styles: styles, search: search}
}

// --- Opening and closing ---

// toggle answers ctrl+j and ctrl+k. Opening starts at the top with the search
// field idle: the numbers are what the menu is for, and typing is the fallback.
func (n *menu) toggle() {
	n.open = !n.open
	n.reset()
}

func (n *menu) close() {
	n.open = false
	n.reset()
}

func (n *menu) reset() {
	n.cursor, n.offset = 0, 0
	n.searching = false
	n.search.SetValue("")
	n.search.Blur()
}

func (n *menu) resize(width, height int) {
	n.width = width
	n.height = height
	n.search.SetWidth(menuInnerWidth(width))
	n.scrollToCursor()
}

// --- Keys ---

// handleKey routes one key press while the menu is up. The menu is modal: what
// it does not recognize is swallowed rather than passed to the screen behind it,
// which is not the one being worked on.
func (n *menu) handleKey(m *model, msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()

	if key == searchKey && !n.searching {
		n.searching = true
		n.cursor = 0
		return n.search.Focus()
	}

	switch msg.Key().Code {
	case tea.KeyEscape:
		// The first escape puts the search field down; the second closes the
		// menu. Typing is a mode, and leaving it should not also lose the menu.
		if n.searching {
			n.stopSearching()
		} else {
			n.close()
		}
		return nil
	case tea.KeyUp:
		n.moveCursor(-1)
		return nil
	case tea.KeyDown:
		n.moveCursor(1)
		return nil
	case tea.KeyEnter:
		return n.openCursor(m)
	}

	// A number is a shortcut until the reader is typing, when it is just a
	// number they typed.
	if !n.searching {
		if chosen, ok := sectionForKey(key); ok {
			n.close()
			return m.openSection(chosen)
		}
		return nil
	}

	search, cmd := n.search.Update(msg)
	n.search = search
	n.clampCursor()
	return cmd
}

func (n *menu) stopSearching() {
	n.searching = false
	n.search.SetValue("")
	n.search.Blur()
	n.clampCursor()
}

func (n *menu) moveCursor(by int) {
	n.cursor = max(min(n.cursor+by, n.count()-1), 0)
	n.scrollToCursor()
}

func (n *menu) clampCursor() {
	n.cursor = max(min(n.cursor, n.count()-1), 0)
	n.scrollToCursor()
}

func (n *menu) openCursor(m *model) tea.Cmd {
	entries := n.entries()
	if n.cursor >= len(entries) {
		return nil
	}
	chosen := entries[n.cursor]
	n.close()
	return chosen.open(m)
}

// --- What is in it ---

// sections are the groups the menu lists, filtered by whatever has been typed.
// A group with nothing left in it is left out rather than drawn empty.
//
// "Recently visited" is not among them. Basecamp keeps it per user on the server
// — a recent_project_visits row per project, fifty of them — but the API does
// not serve that list, so there is nothing here to read it from.
func (n menu) sections() []menuSection {
	places := make([]menuEntry, 0, 1+len(sections))
	places = append(places, menuEntry{
		label: "Home",
		hint:  homeHintText,
		open:  func(m *model) tea.Cmd { return m.goHome() },
	})
	for _, s := range sections {
		places = append(places, menuEntry{
			key:   s.key,
			label: s.label,
			open:  func(m *model) tea.Cmd { return m.openSection(s) },
		})
	}

	projects := make([]menuEntry, 0, len(n.projects))
	for _, p := range n.projects {
		projects = append(projects, menuEntry{
			label: p.name,
			open:  func(m *model) tea.Cmd { return m.openProject(p) },
		})
	}

	groups := []menuSection{{title: "Places", entries: n.matching(places)}}
	if found := n.matching(projects); len(found) > 0 {
		groups = append(groups, menuSection{title: "Projects", entries: found})
	}

	kept := make([]menuSection, 0, len(groups))
	for _, group := range groups {
		if len(group.entries) > 0 {
			kept = append(kept, group)
		}
	}
	return kept
}

// matching narrows a group to what the reader has typed, by label or by key.
func (n menu) matching(entries []menuEntry) []menuEntry {
	query := strings.ToLower(strings.TrimSpace(n.search.Value()))
	if query == "" {
		return entries
	}

	kept := make([]menuEntry, 0, len(entries))
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.label), query) || entry.key == query {
			kept = append(kept, entry)
		}
	}
	return kept
}

// entries is every one the menu is showing, flattened into the order the cursor
// walks them.
func (n menu) entries() []menuEntry {
	var flat []menuEntry
	for _, group := range n.sections() {
		flat = append(flat, group.entries...)
	}
	return flat
}

func (n menu) count() int { return len(n.entries()) }

// --- Rendering ---

// menuRow is one drawn line and which entry it belongs to, so scrolling knows
// where the cursor's row is. A heading belongs to none.
type menuRow struct {
	text  string
	entry int
}

func (n menu) view() string {
	if !n.open {
		return ""
	}
	theme := n.styles.Theme()
	inner := menuInnerWidth(n.width)

	rows := n.layout(inner)
	end := min(n.offset+n.visibleRows(), len(rows))
	lines := make([]string, 0, max(end-n.offset, 0))
	for _, row := range rows[min(n.offset, end):end] {
		lines = append(lines, row.text)
	}

	var b strings.Builder
	b.WriteString(n.searchView(inner))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Border).Render(strings.Repeat("─", inner)))
	for _, line := range lines {
		b.WriteString("\n" + line)
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Padding(0, 1).
		Width(inner + 4).
		Render(b.String())
}

// searchView is the field over the list. It says how to reach it while nobody
// is typing in it, and shows the cursor once somebody is.
func (n menu) searchView(inner int) string {
	if n.searching {
		return n.search.View()
	}
	return fitRow(n.styles.Muted, "Search", n.styles.Muted.Render(searchKey), inner)
}

func (n menu) layout(inner int) []menuRow {
	groups := n.sections()
	if len(groups) == 0 {
		return []menuRow{{text: n.styles.Muted.Render("Nothing matches that"), entry: -1}}
	}

	theme := n.styles.Theme()
	heading := lipgloss.NewStyle().Foreground(theme.Foreground).Bold(true)

	rows := make([]menuRow, 0, 16)
	index := 0
	for _, group := range groups {
		// A dashed rule on the section still growing, so a pause before the next
		// rows appear reads as loading rather than as the end of the list.
		dashed := group.title == "Projects" && n.projectsPaging
		rows = append(rows, menuRow{text: ruledHeading(n.styles, group.title, heading, inner, dashed), entry: -1})

		for _, entry := range group.entries {
			rows = append(rows, menuRow{text: n.entryRow(entry, index, inner), entry: index})
			index++
		}
	}

	// A heading with nothing under it yet, so a menu opened before the first page
	// arrives does not read as an account with no projects in it.
	if len(n.projects) == 0 && n.projectsPaging {
		rows = append(rows, menuRow{
			text:  ruledHeading(n.styles, "Projects", heading, inner, true),
			entry: -1,
		})
	}
	if n.projectsNotice != "" {
		rows = append(rows, menuRow{text: n.styles.Error.Render(truncateToWidth(n.projectsNotice, inner)), entry: -1})
	}
	return rows
}

// entryRow draws one entry: its key, its label, and whatever it wants to say on
// the right. An entry with no key is indented past where the numbers sit, so the
// labels line up under each other.
func (n menu) entryRow(entry menuEntry, index, inner int) string {
	theme := n.styles.Theme()

	label := lipgloss.NewStyle().Foreground(theme.Foreground)
	marker := "  "
	if index == n.cursor {
		marker = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true).Render("› ")
		label = lipgloss.NewStyle().Foreground(theme.Primary).Bold(true)
	}

	key := "  "
	if entry.key != "" {
		key = label.Underline(true).Render(entry.key) + " "
	}

	hint := ""
	if entry.hint != "" {
		hint = n.styles.Muted.Render(entry.hint)
	}
	return marker + key + fitRow(label, entry.label, hint, max(inner-4, 1))
}

// ruledHeading is a heading with rule running out to the right edge, the same
// way the sidebar separates its own groups. A dashed rule means the section is
// still growing.
func ruledHeading(styles *tui.Styles, label string, style lipgloss.Style, width int, dashed bool) string {
	label = truncateToWidth(label, max(width-2, 1))

	rule := width - lipgloss.Width(label) - 1
	if rule < 1 {
		return style.Render(label)
	}

	dash := "─"
	if dashed {
		dash = "┄"
	}
	return style.Render(label) + " " +
		lipgloss.NewStyle().Foreground(styles.Theme().Border).Render(strings.Repeat(dash, rule))
}

// visibleRows is how many rows of the list the box has room for, once the search
// field, its rule and the border have taken theirs.
func (n menu) visibleRows() int {
	return max(n.height-menuTopRow-menuChrome, 1)
}

func (n *menu) scrollToCursor() {
	rows := n.layout(menuInnerWidth(n.width))
	at := -1
	for index, row := range rows {
		if row.entry == n.cursor {
			at = index
			break
		}
	}
	if at < 0 {
		n.offset = 0
		return
	}

	visible := n.visibleRows()
	n.offset = min(n.offset, at)
	if at >= n.offset+visible {
		n.offset = at - visible + 1
	}
	n.offset = max(min(n.offset, max(len(rows)-visible, 0)), 0)
}

func (n menu) helpBindings() []helpBinding {
	if n.searching {
		return []helpBinding{{"↑↓", "move"}, {"enter", "open"}, {"esc", "stop searching"}}
	}
	return []helpBinding{
		{"↑↓", "move"}, {searchKey, "search"}, {"1-4", "go"}, {"enter", "open"}, {"esc", "close"},
	}
}

// menuInnerWidth is the box's content width: three fifths of the screen, less
// the border and padding around it, and never wider than the screen itself.
func menuInnerWidth(screenWidth int) int {
	width := min(max(screenWidth*menuWidthNumerator/menuWidthDenominator, menuMinWidth), screenWidth)
	return max(width-4, 1)
}

// --- Reading the projects ---

// loadProjects reads one page of the projects the account can see.
//
// A positive Page is what keeps this to a single request: left at zero the SDK
// follows the Link chain and hands back every project an account has, which for
// a big one is a long wait for a list the reader may never scroll. They arrive
// in the server's order — most recently created first — and stay in it, because
// sorting a list that arrives a page at a time only shuffles each page against
// itself.
func loadProjects(ctx context.Context, app *appctx.App, page int) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return projectsLoadedMsg{page: page, err: err}
		}

		result, err := app.Account().Projects().List(ctx, &basecamp.ProjectListOptions{Page: page})
		if err != nil {
			return projectsLoadedMsg{page: page, err: err}
		}

		projects := make([]project, 0, len(result.Projects))
		for _, p := range result.Projects {
			projects = append(projects, project{id: p.ID, name: richtext.SanitizeSingleLine(p.Name)})
		}
		return projectsLoadedMsg{page: page, projects: projects}
	}
}
