// Package views provides the individual screens for the workspace TUI.
package views

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Projects is the dashboard view showing all projects with a dock preview.
// When multiple accounts are available, projects are grouped by account
// with section headers.
type Projects struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ProjectInfo]
	styles  *tui.Styles

	list    *widget.List
	split   *widget.SplitPane
	spinner spinner.Model
	loading bool

	// Local rendering data read from pool on update
	projects        []data.ProjectInfo
	projectAccounts map[string]string // projectID -> accountID for navigation

	width, height int
}

// NewProjects creates the projects dashboard view.
func NewProjects(session *workspace.Session) *Projects {
	styles := session.Styles()

	pool := session.Hub().Projects()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No projects found. Try 'bcq projects list' to verify access.")
	list.SetFocused(true)

	split := widget.NewSplitPane(styles, 0.35)

	snap := pool.Get()

	v := &Projects{
		session:         session,
		pool:            pool,
		styles:          styles,
		list:            list,
		split:           split,
		spinner:         s,
		loading:         !snap.Usable(),
		projectAccounts: make(map[string]string),
	}

	if snap.Usable() {
		v.projects = snap.Data
		v.syncProjectList()
	}

	return v
}

// Title implements View.
func (v *Projects) Title() string {
	return "Projects"
}

// ShortHelp implements View.
func (v *Projects) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "bookmark")),
	}
}

// FullHelp implements View.
func (v *Projects) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Projects) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Projects) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *Projects) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.split.SetSize(w, h)
	v.list.SetSize(v.split.LeftWidth(), h)
}

// Init implements tea.Model.
func (v *Projects) Init() tea.Cmd {
	cmds := []tea.Cmd{v.spinner.Tick}

	snap := v.pool.Get()
	if snap.Usable() {
		v.projects = snap.Data
		v.syncProjectList()
		v.loading = false
		if snap.Fresh() {
			return tea.Batch(cmds...)
		}
	}

	cmds = append(cmds, v.pool.FetchIfStale(v.session.Hub().Global().Context()))
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (v *Projects) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.projects = snap.Data
				v.syncProjectList()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading projects")
			}
		}
		return v, nil

	case workspace.ProjectBookmarkedMsg:
		if msg.Err != nil {
			// Revert optimistic update
			p := v.findProject(msg.ProjectID)
			if p != nil {
				p.Bookmarked = !msg.Bookmarked
				v.syncProjectList()
			}
			return v, workspace.ReportError(msg.Err, "toggling bookmark")
		}
		// On success, invalidate pool so other views (Home bookmarks) get updated data
		v.pool.Invalidate()
		return v, workspace.SetStatus("Bookmark updated", false)

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().Global().Context()))

	case spinner.TickMsg:
		if v.loading {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		if v.loading {
			return v, nil
		}

		keys := workspace.DefaultListKeyMap()
		switch {
		case key.Matches(msg, keys.Open):
			if item := v.list.Selected(); item != nil {
				return v, v.openProject(item.ID)
			}
		case msg.String() == "b":
			return v, v.toggleBookmark()
		default:
			cmd := v.list.Update(msg)
			return v, cmd
		}
	}
	return v, nil
}

// View implements tea.Model.
func (v *Projects) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading projects...")
	}

	// Left panel: project list
	left := v.list.View()

	// Right panel: dock preview for selected project
	right := v.renderDockPreview()

	v.split.SetContent(left, right)
	return v.split.View()
}

func (v *Projects) syncProjectList() {
	v.projectAccounts = make(map[string]string)

	// Detect multi-account
	accounts := make(map[string]string) // ID -> Name
	for _, p := range v.projects {
		accounts[p.AccountID] = p.AccountName
	}
	multiAccount := len(accounts) > 1

	var items []widget.ListItem

	if multiAccount {
		// Group by account
		type group struct {
			name     string
			projects []data.ProjectInfo
		}
		var groups []group
		seen := make(map[string]int)
		for _, p := range v.projects {
			if idx, ok := seen[p.AccountID]; ok {
				groups[idx].projects = append(groups[idx].projects, p)
			} else {
				seen[p.AccountID] = len(groups)
				groups = append(groups, group{name: p.AccountName, projects: []data.ProjectInfo{p}})
			}
		}
		for _, g := range groups {
			items = append(items, widget.ListItem{Title: g.name, Header: true})
			// Bookmarked first within each group
			var bm, reg []data.ProjectInfo
			for _, p := range g.projects {
				if p.Bookmarked {
					bm = append(bm, p)
				} else {
					reg = append(reg, p)
				}
			}
			for _, p := range append(bm, reg...) {
				id := fmt.Sprintf("%d", p.ID)
				v.projectAccounts[id] = p.AccountID
				items = append(items, projectInfoToListItem(p))
			}
		}
	} else {
		// Single account: bookmarked first
		var bm, reg []data.ProjectInfo
		for _, p := range v.projects {
			if p.Bookmarked {
				bm = append(bm, p)
			} else {
				reg = append(reg, p)
			}
		}
		for _, p := range append(bm, reg...) {
			id := fmt.Sprintf("%d", p.ID)
			v.projectAccounts[id] = p.AccountID
			items = append(items, projectInfoToListItem(p))
		}
	}

	v.list.SetItems(items)
}

func projectInfoToListItem(p data.ProjectInfo) widget.ListItem {
	desc := p.Purpose
	if desc == "" {
		desc = p.Description
	}
	if len(desc) > 60 {
		desc = desc[:57] + "..."
	}
	return widget.ListItem{
		ID:          fmt.Sprintf("%d", p.ID),
		Title:       p.Name,
		Description: desc,
		Marked:      p.Bookmarked,
	}
}

func (v *Projects) findProject(projectID int64) *data.ProjectInfo {
	for i := range v.projects {
		if v.projects[i].ID == projectID {
			return &v.projects[i]
		}
	}
	return nil
}

func (v *Projects) renderDockPreview() string {
	if v.split.IsCollapsed() {
		return ""
	}

	item := v.list.Selected()
	if item == nil {
		return ""
	}

	// Find the full project from local data
	var projectID int64
	fmt.Sscanf(item.ID, "%d", &projectID)
	project := v.findProject(projectID)
	if project == nil {
		return ""
	}

	theme := v.styles.Theme()
	w := v.split.RightWidth() - 2 // padding

	var b strings.Builder

	// Project name
	b.WriteString(lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Primary).
		Width(w).
		Render(project.Name))
	b.WriteString("\n")

	// Account badge (multi-account mode)
	accounts := make(map[string]struct{})
	for _, p := range v.projects {
		accounts[p.AccountID] = struct{}{}
	}
	if len(accounts) > 1 {
		if project.AccountName != "" {
			b.WriteString(lipgloss.NewStyle().
				Foreground(theme.Muted).
				Render(project.AccountName))
			b.WriteString("\n")
		}
	}

	// Purpose
	if project.Purpose != "" {
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(w).
			Render(project.Purpose))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	// Dock tools
	if len(project.Dock) > 0 {
		for _, tool := range project.Dock {
			if !tool.Enabled {
				continue
			}
			name := dockToolDisplayName(tool.Name)
			line := lipgloss.NewStyle().
				Foreground(theme.Foreground).
				Render("  " + name)
			if tool.Title != "" && tool.Title != name {
				line += lipgloss.NewStyle().
					Foreground(theme.Muted).
					Render(" - " + tool.Title)
			}
			b.WriteString(line + "\n")
		}
	} else {
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("  No dock tools loaded"))
	}

	return lipgloss.NewStyle().Padding(0, 1).Render(b.String())
}

func dockToolDisplayName(name string) string {
	switch name {
	case "todoset":
		return "Todos"
	case "message_board":
		return "Message Board"
	case "chat":
		return "Campfire"
	case "schedule":
		return "Schedule"
	case "questionnaire":
		return "Check-ins"
	case "vault":
		return "Docs & Files"
	case "kanban_board":
		return "Card Table"
	case "inbox":
		return "Email Forwards"
	default:
		return strings.ReplaceAll(name, "_", " ")
	}
}

func (v *Projects) toggleBookmark() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	var projectID int64
	fmt.Sscanf(item.ID, "%d", &projectID)

	p := v.findProject(projectID)
	if p == nil {
		return nil
	}

	newBookmarked := !p.Bookmarked
	// Optimistic: flip in local data, re-sort
	p.Bookmarked = newBookmarked
	v.syncProjectList()

	return v.setBookmark(projectID, newBookmarked)
}

func (v *Projects) setBookmark(projectID int64, bookmarked bool) tea.Cmd {
	accountID := v.session.Scope().AccountID
	if aid, ok := v.projectAccounts[fmt.Sprintf("%d", projectID)]; ok && aid != "" {
		accountID = aid
	}

	ctx := v.session.Context()
	client := v.session.MultiStore().ClientFor(accountID)
	if client == nil {
		client = v.session.AccountClient()
	}
	return func() tea.Msg {

		var err error
		path := fmt.Sprintf("/projects/%d/star.json", projectID)
		if bookmarked {
			_, err = client.Post(ctx, path, nil)
		} else {
			_, err = client.Delete(ctx, path)
		}

		return workspace.ProjectBookmarkedMsg{
			ProjectID:  projectID,
			Bookmarked: bookmarked,
			Err:        err,
		}
	}
}

func (v *Projects) openProject(id string) tea.Cmd {
	var projectID int64
	fmt.Sscanf(id, "%d", &projectID)

	project := v.findProject(projectID)
	if project == nil {
		return nil
	}

	scope := v.session.Scope()
	scope.ProjectID = projectID
	scope.ProjectName = project.Name

	// In multi-account mode, set the correct account for this project
	if acctID, ok := v.projectAccounts[id]; ok && acctID != "" {
		scope.AccountID = acctID
		scope.AccountName = project.AccountName
	}

	// Record in recents AFTER resolving the correct account
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:        id,
			Title:     project.Name,
			Type:      recents.TypeProject,
			AccountID: scope.AccountID,
		})
	}

	return workspace.Navigate(workspace.ViewDock, scope)
}
