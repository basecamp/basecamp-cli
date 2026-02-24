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
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Projects is the dashboard view showing all projects with an inline dock.
// When multiple accounts are available, projects are grouped by account
// with section headers.
//
// The view has two focus phases:
//   - Left panel (default): navigate projects with j/k, Enter/l opens inline dock
//   - Right panel (inline dock): navigate tools with j/k, Enter opens tool view
type Projects struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ProjectInfo]
	styles  *tui.Styles

	list    *widget.List
	split   *widget.SplitPane
	spinner spinner.Model
	loading bool

	// Inline dock (right panel)
	toolList        *widget.List
	focusRight      bool
	selectedProject *data.ProjectInfo
	dockKeys        dockKeyMap

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
	list.SetEmptyMessage(empty.NoProjects())
	list.SetFocused(true)

	toolList := widget.NewList(styles)
	toolList.SetEmptyText("No tools enabled")
	toolList.SetFocused(false)

	split := widget.NewSplitPane(styles, 0.35)

	snap := pool.Get()

	v := &Projects{
		session:         session,
		pool:            pool,
		styles:          styles,
		list:            list,
		toolList:        toolList,
		split:           split,
		spinner:         s,
		loading:         !snap.Usable(),
		dockKeys:        defaultDockKeyMap(),
		projectAccounts: make(map[string]string),
	}

	if snap.Usable() {
		v.projects = snap.Data
		v.syncProjectList()
		v.updateSelectedProject()
	}

	return v
}

// Title implements View.
func (v *Projects) Title() string {
	return "Projects"
}

// ShortHelp implements View.
func (v *Projects) ShortHelp() []key.Binding {
	if v.list.Filtering() || v.toolList.Filtering() {
		return filterHints()
	}
	if v.focusRight {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
			v.dockKeys.Todos,
			v.dockKeys.Campfire,
			v.dockKeys.Messages,
			v.dockKeys.Cards,
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "activity")),
		}
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
func (v *Projects) StartFilter() {
	if v.focusRight {
		v.toolList.StartFilter()
	} else {
		v.list.StartFilter()
	}
}

// InputActive implements workspace.InputCapturer.
func (v *Projects) InputActive() bool {
	return v.list.Filtering() || v.toolList.Filtering()
}

// IsModal implements workspace.ModalActive.
// When the right panel (inline dock) is focused, Esc returns to the project
// list instead of triggering global back navigation.
func (v *Projects) IsModal() bool {
	return v.focusRight
}

// SetSize implements View.
func (v *Projects) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.split.SetSize(w, h)
	v.list.SetSize(v.split.LeftWidth(), h)
	v.resizeToolList()
}

// resizeToolList computes and applies the correct size for the right-panel tool list.
// Called from SetSize() and after project selection changes — never from View().
func (v *Projects) resizeToolList() {
	if v.selectedProject == nil {
		return
	}
	var w int
	if v.split.IsCollapsed() {
		w = max(0, v.width-2)
	} else {
		w = max(0, v.split.RightWidth()-2)
	}
	header := v.renderToolHeader(w)
	headerLines := strings.Count(header, "\n") + 2
	v.toolList.SetSize(w, max(1, v.height-headerLines))
}

// Init implements tea.Model.
func (v *Projects) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 2)
	cmds = append(cmds, v.spinner.Tick)

	snap := v.pool.Get()
	if snap.Usable() {
		v.projects = snap.Data
		v.syncProjectList()
		v.updateSelectedProject()
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
				v.afterPoolUpdate()
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

	case workspace.FocusMsg:
		return v, v.pool.FetchIfStale(v.session.Hub().Global().Context())

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
		if v.focusRight {
			return v, v.handleToolKey(msg)
		}
		return v, v.handleProjectKey(msg)
	}
	return v, nil
}

// handleProjectKey processes keys when the left (project) panel is focused.
func (v *Projects) handleProjectKey(msg tea.KeyMsg) tea.Cmd {
	if v.list.Filtering() {
		return v.list.Update(msg)
	}
	keys := workspace.DefaultListKeyMap()
	switch {
	case key.Matches(msg, keys.Open), msg.String() == "l":
		if item := v.list.Selected(); item != nil {
			v.enterDock(item.ID)
		}
		return nil
	case msg.String() == "b":
		return v.toggleBookmark()
	default:
		prevIdx := v.list.SelectedIndex()
		cmd := v.list.Update(msg)
		if v.list.SelectedIndex() != prevIdx {
			v.updateSelectedProject()
		}
		return cmd
	}
}

// handleToolKey processes keys when the right (tool) panel is focused.
func (v *Projects) handleToolKey(msg tea.KeyMsg) tea.Cmd {
	// When filtering, let the tool list handle all keys (esc exits filter,
	// typing narrows results, enter confirms). Don't intercept hotkeys.
	if v.toolList.Filtering() {
		return v.toolList.Update(msg)
	}

	dk := v.dockKeys
	listKeys := workspace.DefaultListKeyMap()
	globalKeys := workspace.DefaultGlobalKeyMap()

	switch {
	case key.Matches(msg, globalKeys.Back), msg.String() == "h":
		v.leaveDock()
		return nil
	case key.Matches(msg, dk.Todos):
		return v.navigateToTool("todoset", workspace.ViewTodos)
	case key.Matches(msg, dk.Campfire):
		return v.navigateToTool("chat", workspace.ViewCampfire)
	case key.Matches(msg, dk.Messages):
		return v.navigateToTool("message_board", workspace.ViewMessages)
	case key.Matches(msg, dk.Cards):
		return v.navigateToTool("kanban_board", workspace.ViewCards)
	case key.Matches(msg, dk.Schedule):
		return v.navigateToTool("schedule", workspace.ViewSchedule)
	case msg.String() == "a":
		if v.selectedProject == nil {
			return nil
		}
		scope := v.projectScope()
		v.recordProjectVisit(scope)
		return workspace.Navigate(workspace.ViewTimeline, scope)
	case key.Matches(msg, listKeys.Open):
		return v.openTool()
	default:
		return v.toolList.Update(msg)
	}
}

// enterDock shifts focus to the right panel (inline dock) for the given project.
func (v *Projects) enterDock(itemID string) {
	var projectID int64
	fmt.Sscanf(itemID, "%d", &projectID)
	project := v.findProject(projectID)
	if project == nil {
		return
	}

	v.selectedProject = project
	v.focusRight = true
	v.list.SetFocused(false)
	v.toolList.SetFocused(true)
	v.syncToolList()
	v.resizeToolList()
}

// leaveDock returns focus to the left panel (project list).
func (v *Projects) leaveDock() {
	v.focusRight = false
	v.toolList.SetFocused(false)
	v.toolList.StopFilter()
	v.list.SetFocused(true)
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

	// Collapsed mode: show one panel at a time
	if v.split.IsCollapsed() && v.focusRight {
		w := max(0, v.width-2) // padding
		header := v.renderToolHeader(w)
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(0, 1).
			Render(header + "\n\n" + v.toolList.View())
	}

	// Left panel: project list
	left := v.list.View()

	// Right panel: inline dock (tool list with project header)
	right := v.renderRightPanel()

	v.split.SetContent(left, right)
	return v.split.View()
}

// renderRightPanel renders the tool list with a project header for the right pane.
func (v *Projects) renderRightPanel() string {
	if v.split.IsCollapsed() {
		return ""
	}

	if v.selectedProject == nil {
		return ""
	}

	w := max(0, v.split.RightWidth()-2) // padding
	header := v.renderToolHeader(w)

	return lipgloss.NewStyle().Padding(0, 1).Render(
		header + "\n\n" + v.toolList.View(),
	)
}

// renderToolHeader renders the project name, account badge, and purpose above the tool list.
func (v *Projects) renderToolHeader(w int) string {
	if v.selectedProject == nil {
		return ""
	}

	theme := v.styles.Theme()
	var b strings.Builder

	// Project name
	b.WriteString(lipgloss.NewStyle().
		Bold(true).
		Foreground(theme.Primary).
		Width(w).
		Render(v.selectedProject.Name))

	// Account badge (multi-account mode)
	if v.isMultiAccount() && v.selectedProject.AccountName != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render(v.selectedProject.AccountName))
	}

	// Purpose
	if v.selectedProject.Purpose != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Width(w).
			Render(v.selectedProject.Purpose))
	}

	return b.String()
}

func (v *Projects) isMultiAccount() bool {
	accounts := make(map[string]struct{})
	for _, p := range v.projects {
		accounts[p.AccountID] = struct{}{}
	}
	return len(accounts) > 1
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
	desc = widget.Truncate(desc, 57)
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

// updateSelectedProject syncs selectedProject and tool list to the current
// project list cursor. Called when the cursor moves or data refreshes.
func (v *Projects) updateSelectedProject() {
	item := v.list.Selected()
	if item == nil {
		v.selectedProject = nil
		v.toolList.SetItems(nil)
		return
	}
	var projectID int64
	fmt.Sscanf(item.ID, "%d", &projectID)
	v.selectedProject = v.findProject(projectID)
	v.syncToolList()
	v.resizeToolList()
}

// afterPoolUpdate handles right-panel state after a data refresh.
// Rebinds selectedProject to the new slice so tool data stays current.
func (v *Projects) afterPoolUpdate() {
	if v.focusRight {
		if v.selectedProject == nil {
			return
		}
		fresh := v.findProject(v.selectedProject.ID)
		if fresh == nil {
			v.leaveDock()
			v.selectedProject = nil
			v.toolList.SetItems(nil)
			return
		}
		v.selectedProject = fresh
		v.syncToolList()
		return
	}
	v.updateSelectedProject()
}

// syncToolList populates the tool list from the selected project's dock.
func (v *Projects) syncToolList() {
	if v.selectedProject == nil {
		v.toolList.SetItems(nil)
		return
	}

	var items []widget.ListItem
	for _, tool := range v.selectedProject.Dock {
		if !tool.Enabled {
			continue
		}
		title := tool.Title
		if title == "" {
			title = dockToolDisplayName(tool.Name)
		}
		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", tool.ID),
			Title:       title,
			Description: dockToolDisplayName(tool.Name),
			Extra:       toolHotkey(tool.Name),
		})
	}
	v.toolList.SetItems(items)
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

// toolHotkey returns the single-key shortcut for a dock tool, or "".
func toolHotkey(name string) string {
	switch name {
	case "todoset":
		return "t"
	case "chat":
		return "c"
	case "message_board":
		return "m"
	case "kanban_board":
		return "k"
	case "schedule":
		return "s"
	default:
		return ""
	}
}

// toolNameToView maps a dock tool API name to its ViewTarget.
func toolNameToView(name string) (workspace.ViewTarget, bool) {
	switch name {
	case "todoset":
		return workspace.ViewTodos, true
	case "chat":
		return workspace.ViewCampfire, true
	case "message_board":
		return workspace.ViewMessages, true
	case "kanban_board":
		return workspace.ViewCards, true
	case "schedule":
		return workspace.ViewSchedule, true
	case "vault":
		return workspace.ViewDocsFiles, true
	case "questionnaire":
		return workspace.ViewCheckins, true
	case "inbox":
		return workspace.ViewForwards, true
	default:
		return 0, false
	}
}

// projectScope builds a navigation scope for the selected project.
func (v *Projects) projectScope() workspace.Scope {
	scope := v.session.Scope()
	scope.ProjectID = v.selectedProject.ID
	scope.ProjectName = v.selectedProject.Name
	if acctID, ok := v.projectAccounts[fmt.Sprintf("%d", v.selectedProject.ID)]; ok && acctID != "" {
		scope.AccountID = acctID
		scope.AccountName = v.selectedProject.AccountName
	}
	return scope
}

// recordProjectVisit adds the selected project to recents.
func (v *Projects) recordProjectVisit(scope workspace.Scope) {
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:        fmt.Sprintf("%d", v.selectedProject.ID),
			Title:     v.selectedProject.Name,
			Type:      recents.TypeProject,
			AccountID: scope.AccountID,
		})
	}
}

// openTool navigates directly to the tool selected in the tool list.
func (v *Projects) openTool() tea.Cmd {
	item := v.toolList.Selected()
	if item == nil || v.selectedProject == nil {
		return nil
	}

	var toolID int64
	fmt.Sscanf(item.ID, "%d", &toolID)

	for _, tool := range v.selectedProject.Dock {
		if tool.ID == toolID {
			target, ok := toolNameToView(tool.Name)
			if !ok {
				return workspace.SetStatus(dockToolDisplayName(tool.Name), false)
			}
			scope := v.projectScope()
			scope.ToolType = tool.Name
			scope.ToolID = tool.ID
			v.recordProjectVisit(scope)
			return workspace.Navigate(target, scope)
		}
	}
	return nil
}

// navigateToTool jumps to a tool by name (used by dock hotkeys).
func (v *Projects) navigateToTool(toolName string, target workspace.ViewTarget) tea.Cmd {
	if v.selectedProject == nil {
		return nil
	}

	for _, tool := range v.selectedProject.Dock {
		if tool.Name == toolName && tool.Enabled {
			scope := v.projectScope()
			scope.ToolType = toolName
			scope.ToolID = tool.ID
			v.recordProjectVisit(scope)
			return workspace.Navigate(target, scope)
		}
	}

	return workspace.SetStatus(
		fmt.Sprintf("No %s in this project", strings.ReplaceAll(toolName, "_", " ")), true)
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
