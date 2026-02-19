// Package views provides the individual screens for the workspace TUI.
package views

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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
	store   *data.Store
	styles  *tui.Styles

	list    *widget.List
	split   *widget.SplitPane
	spinner spinner.Model
	loading bool

	// Multi-account state
	multiAccount    bool                            // true when showing projects from all accounts
	accountGroups   []workspace.AccountProjectGroup // grouped projects by account
	projectAccounts map[string]string               // projectID -> accountID for navigation

	width, height int
}

// NewProjects creates the projects dashboard view.
func NewProjects(session *workspace.Session, store *data.Store) *Projects {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No projects found. Try 'bcq projects list' to verify access.")
	list.SetFocused(true)

	split := widget.NewSplitPane(styles, 0.35)

	v := &Projects{
		session:         session,
		store:           store,
		styles:          styles,
		list:            list,
		split:           split,
		spinner:         s,
		loading:         !store.HasProjects(),
		projectAccounts: make(map[string]string),
	}

	if store.HasProjects() {
		v.syncFromStore()
	}

	return v
}

// Title implements View.
func (v *Projects) Title() string {
	return "Projects"
}

// ShortHelp implements View.
func (v *Projects) ShortHelp() []key.Binding {
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

	// Try multi-account fetch if accounts are already discovered
	ms := v.session.MultiStore()
	if accounts := ms.Accounts(); len(accounts) > 1 {
		cmds = append(cmds, v.fetchMultiAccountProjects())
	} else if !v.store.HasProjects() {
		cmds = append(cmds, v.fetchProjects())
	}

	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (v *Projects) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.ProjectsLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading projects")
		}
		v.store.SetProjects(msg.Projects)
		v.syncFromStore()
		return v, nil

	case workspace.MultiAccountProjectsLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading projects")
		}
		v.multiAccount = true
		v.accountGroups = msg.AccountProjects
		v.syncFromMultiAccount()
		return v, nil

	case workspace.ProjectBookmarkedMsg:
		if msg.Err != nil {
			// Revert optimistic update
			p := v.store.Project(msg.ProjectID)
			if p != nil {
				p.Bookmarked = !msg.Bookmarked
				v.store.UpsertProject(*p)
				v.updateAccountGroupProject(msg.ProjectID, !msg.Bookmarked)
				v.resyncList()
			}
			return v, workspace.ReportError(msg.Err, "toggling bookmark")
		}
		return v, workspace.SetStatus("Bookmark updated", false)

	case workspace.RefreshMsg:
		v.loading = true
		ms := v.session.MultiStore()
		if accounts := ms.Accounts(); len(accounts) > 1 {
			return v, tea.Batch(v.spinner.Tick, v.fetchMultiAccountProjects())
		}
		return v, tea.Batch(v.spinner.Tick, v.fetchProjects())

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

func (v *Projects) syncFromStore() {
	projects := v.store.Projects()
	items := make([]widget.ListItem, 0, len(projects))

	// Bookmarked first, then alphabetical
	var bookmarked, regular []basecamp.Project
	for _, p := range projects {
		if p.Bookmarked {
			bookmarked = append(bookmarked, p)
		} else {
			regular = append(regular, p)
		}
	}

	for _, p := range bookmarked {
		items = append(items, projectToListItem(p, true))
	}
	for _, p := range regular {
		items = append(items, projectToListItem(p, false))
	}

	v.list.SetItems(items)
}

func (v *Projects) syncFromMultiAccount() {
	v.projectAccounts = make(map[string]string)
	var items []widget.ListItem
	multipleAccounts := len(v.accountGroups) > 1

	// Also populate the store with projects from the current account for dock preview
	for _, group := range v.accountGroups {
		if multipleAccounts {
			items = append(items, widget.ListItem{
				Title:  group.Account.Name,
				Header: true,
			})
		}

		// Bookmarked first within each account group
		var bookmarked, regular []basecamp.Project
		for _, p := range group.Projects {
			if p.Bookmarked {
				bookmarked = append(bookmarked, p)
			} else {
				regular = append(regular, p)
			}
		}

		for _, p := range bookmarked {
			id := fmt.Sprintf("%d", p.ID)
			v.projectAccounts[id] = group.Account.ID
			items = append(items, projectToListItem(p, true))
		}
		for _, p := range regular {
			id := fmt.Sprintf("%d", p.ID)
			v.projectAccounts[id] = group.Account.ID
			items = append(items, projectToListItem(p, false))
		}

		// Upsert into store for dock preview
		for _, p := range group.Projects {
			v.store.UpsertProject(p)
		}
	}

	v.list.SetItems(items)
}

func projectToListItem(p basecamp.Project, bookmarked bool) widget.ListItem {
	desc := p.Purpose
	if desc == "" {
		desc = p.Description
	}
	// Truncate long descriptions
	if len(desc) > 60 {
		desc = desc[:57] + "..."
	}

	return widget.ListItem{
		ID:          fmt.Sprintf("%d", p.ID),
		Title:       p.Name,
		Description: desc,
		Marked:      bookmarked,
	}
}

func (v *Projects) renderDockPreview() string {
	if v.split.IsCollapsed() {
		return ""
	}

	item := v.list.Selected()
	if item == nil {
		return ""
	}

	// Find the full project from store
	var projectID int64
	fmt.Sscanf(item.ID, "%d", &projectID)
	project := v.store.Project(projectID)
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
	if v.multiAccount {
		if acctID, ok := v.projectAccounts[item.ID]; ok {
			for _, g := range v.accountGroups {
				if g.Account.ID == acctID {
					b.WriteString(lipgloss.NewStyle().
						Foreground(theme.Muted).
						Render(g.Account.Name))
					b.WriteString("\n")
					break
				}
			}
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

func (v *Projects) fetchProjects() tea.Cmd {
	cache := v.store.Cache()
	entry := cache.Get("projects:list")

	if entry != nil {
		projects, _ := entry.Value.([]basecamp.Project) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.ProjectsLoadedMsg{Projects: projects}
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return workspace.ProjectsLoadedMsg{Projects: projects}
			},
			v.fetchProjectsFromAPI(),
		)
	}

	return v.fetchProjectsFromAPI()
}

func (v *Projects) fetchProjectsFromAPI() tea.Cmd {
	if !v.session.HasAccount() {
		return func() tea.Msg {
			return workspace.ProjectsLoadedMsg{
				Err: fmt.Errorf("no account selected"),
			}
		}
	}
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		result, err := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
		if err != nil {
			return workspace.ProjectsLoadedMsg{Err: err}
		}
		v.store.Cache().Set("projects:list", result.Projects, 30*time.Second, 5*time.Minute)
		return workspace.ProjectsLoadedMsg{Projects: result.Projects}
	}
}

func (v *Projects) fetchMultiAccountProjects() tea.Cmd {
	cache := v.session.MultiStore().Cache()
	entry := cache.Get("projects:multi")

	if entry != nil {
		groups, _ := entry.Value.([]workspace.AccountProjectGroup) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.MultiAccountProjectsLoadedMsg{AccountProjects: groups}
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return workspace.MultiAccountProjectsLoadedMsg{AccountProjects: groups}
			},
			v.fetchMultiAccountProjectsFromAPI(),
		)
	}

	return v.fetchMultiAccountProjectsFromAPI()
}

func (v *Projects) fetchMultiAccountProjectsFromAPI() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	return func() tea.Msg {
		results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
			result, err := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
			if err != nil {
				return nil, err
			}
			return result.Projects, nil
		})

		var groups []workspace.AccountProjectGroup
		for _, r := range results {
			if r.Err != nil {
				continue // skip failed accounts
			}
			projects, _ := r.Data.([]basecamp.Project)
			if len(projects) == 0 {
				continue
			}
			groups = append(groups, workspace.AccountProjectGroup{
				Account:  workspace.AccountInfo{ID: r.Account.ID, Name: r.Account.Name},
				Projects: projects,
			})
		}

		ms.Cache().Set("projects:multi", groups, 30*time.Second, 5*time.Minute)
		return workspace.MultiAccountProjectsLoadedMsg{AccountProjects: groups}
	}
}

func (v *Projects) toggleBookmark() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	var projectID int64
	fmt.Sscanf(item.ID, "%d", &projectID)
	project := v.store.Project(projectID)
	if project == nil {
		return nil
	}

	newBookmarked := !project.Bookmarked

	// Optimistic update: flip in store and account groups, then re-sort
	project.Bookmarked = newBookmarked
	v.store.UpsertProject(*project)
	v.updateAccountGroupProject(projectID, newBookmarked)
	v.resyncList()

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

// updateAccountGroupProject updates the Bookmarked field for a project within
// accountGroups, keeping the multi-account view data consistent.
func (v *Projects) updateAccountGroupProject(projectID int64, bookmarked bool) {
	for gi := range v.accountGroups {
		for pi := range v.accountGroups[gi].Projects {
			if v.accountGroups[gi].Projects[pi].ID == projectID {
				v.accountGroups[gi].Projects[pi].Bookmarked = bookmarked
				return
			}
		}
	}
}

// resyncList rebuilds the list items from the appropriate source.
func (v *Projects) resyncList() {
	if v.multiAccount {
		v.syncFromMultiAccount()
	} else {
		v.syncFromStore()
	}
}

func (v *Projects) openProject(id string) tea.Cmd {
	var projectID int64
	fmt.Sscanf(id, "%d", &projectID)

	project := v.store.Project(projectID)
	if project == nil {
		return nil
	}

	scope := v.session.Scope()
	scope.ProjectID = projectID
	scope.ProjectName = project.Name

	// In multi-account mode, set the correct account for this project
	if acctID, ok := v.projectAccounts[id]; ok && acctID != "" {
		scope.AccountID = acctID
		for _, g := range v.accountGroups {
			if g.Account.ID == acctID {
				scope.AccountName = g.Account.Name
				break
			}
		}
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
