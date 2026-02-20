package views

import (
	"fmt"
	"strconv"
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

// Home is a combined dashboard view that shows recents, activity, assignments,
// and bookmarked projects in a single scrollable list with section headers.
// It renders instantly with local recents data, then progressively fills in
// API-sourced sections via shared Hub pools.
type Home struct {
	session *workspace.Session
	styles  *tui.Styles

	heyPool     *data.Pool[[]data.ActivityEntryInfo]
	assignPool  *data.Pool[[]data.AssignmentInfo]
	projectPool *data.Pool[[]data.ProjectInfo]

	list    *widget.List
	spinner spinner.Model

	// Section data (populated progressively)
	recentItems   []widget.ListItem // instant from recents store
	heyItems      []widget.ListItem // from API
	assignItems   []widget.ListItem // from API
	bookmarkItems []widget.ListItem // from API

	// Metadata for navigation
	itemMeta map[string]homeItemMeta

	width, height int
}

type homeItemMeta struct {
	accountID   string
	projectID   int64
	recordingID int64
	recordType  string
	viewTarget  workspace.ViewTarget
}

// NewHome creates the home dashboard view.
func NewHome(session *workspace.Session) *Home {
	styles := session.Styles()

	hub := session.Hub()
	heyPool := hub.HeyActivity()
	assignPool := hub.Assignments()
	projectPool := hub.Projects()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)

	v := &Home{
		session:     session,
		styles:      styles,
		heyPool:     heyPool,
		assignPool:  assignPool,
		projectPool: projectPool,
		list:        list,
		spinner:     s,
		itemMeta:    make(map[string]homeItemMeta),
	}

	v.syncRecents()

	return v
}

func (v *Home) Title() string { return "Home" }

func (v *Home) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}

	// Loading state — no misleading hints while spinner is on screen
	if v.anyLoading() && v.list.Len() == 0 {
		return nil
	}

	item := v.list.Selected()
	if item == nil {
		return v.defaultHints()
	}

	// Section headers: just navigation + projects
	if item.Header {
		return []key.Binding{
			key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
			key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "projects")),
		}
	}

	meta, ok := v.itemMeta[item.ID]
	if !ok {
		return v.defaultHints()
	}

	// Contextual "enter" label based on item type
	enterDesc := "open"
	switch meta.viewTarget {
	case workspace.ViewDock:
		enterDesc = "open project"
	case workspace.ViewCampfire:
		enterDesc = "open campfire"
	default:
		if meta.recordType != "" {
			label := strings.ToLower(meta.recordType)
			if len(label) > 15 {
				label = label[:15]
			}
			enterDesc = "open " + label
		}
	}

	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", enterDesc)),
		key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "projects")),
	}
}

func (v *Home) defaultHints() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "projects")),
	}
}

func (v *Home) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

func (v *Home) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// StartFilter implements Filterable.
func (v *Home) StartFilter() { v.list.StartFilter() }

// InputActive implements InputCapturer.
func (v *Home) InputActive() bool { return v.list.Filtering() }

func (v *Home) Init() tea.Cmd {
	cmds := []tea.Cmd{v.spinner.Tick}
	globalCtx := v.session.Hub().Global().Context()

	// Hey activity
	snap := v.heyPool.Get()
	if snap.Usable() {
		v.syncHeyEntries(snap.Data)
	}
	if !snap.Fresh() {
		cmds = append(cmds, v.heyPool.FetchIfStale(globalCtx))
	}

	// Assignments
	snap2 := v.assignPool.Get()
	if snap2.Usable() {
		v.syncAssignments(snap2.Data)
	}
	if !snap2.Fresh() {
		cmds = append(cmds, v.assignPool.FetchIfStale(globalCtx))
	}

	// Projects/Bookmarks
	snap3 := v.projectPool.Get()
	if snap3.Usable() {
		v.syncBookmarks(snap3.Data)
	}
	if !snap3.Fresh() {
		cmds = append(cmds, v.projectPool.FetchIfStale(globalCtx))
	}

	v.rebuildList()
	return tea.Batch(cmds...)
}

func (v *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		switch msg.Key {
		case v.heyPool.Key():
			snap := v.heyPool.Get()
			if snap.Usable() {
				v.syncHeyEntries(snap.Data)
			} else if snap.State == data.StateError {
				v.heyItems = nil // clear section, stop loading
			}
		case v.assignPool.Key():
			snap := v.assignPool.Get()
			if snap.Usable() {
				v.syncAssignments(snap.Data)
			} else if snap.State == data.StateError {
				v.assignItems = nil
			}
		case v.projectPool.Key():
			snap := v.projectPool.Get()
			if snap.Usable() {
				v.syncBookmarks(snap.Data)
			} else if snap.State == data.StateError {
				v.bookmarkItems = nil
			}
		}
		v.rebuildList()
		return v, nil

	case workspace.RefreshMsg:
		v.heyPool.Invalidate()
		v.assignPool.Invalidate()
		v.projectPool.Invalidate()
		globalCtx := v.session.Hub().Global().Context()
		v.syncRecents()
		v.rebuildList()
		return v, tea.Batch(
			v.spinner.Tick,
			v.heyPool.Fetch(globalCtx),
			v.assignPool.Fetch(globalCtx),
			v.projectPool.Fetch(globalCtx),
		)

	case workspace.FocusMsg:
		v.syncRecents()
		v.rebuildList()
		return v, nil

	case spinner.TickMsg:
		if v.anyLoading() {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		if v.list.Filtering() {
			return v, v.list.Update(msg)
		}
		keys := workspace.DefaultListKeyMap()
		switch {
		case key.Matches(msg, keys.Open):
			return v, v.openSelected()
		case msg.String() == "p":
			return v, workspace.Navigate(workspace.ViewProjects, v.session.Scope())
		default:
			return v, v.list.Update(msg)
		}
	}
	return v, nil
}

func (v *Home) View() string {
	// Show spinner only when all sections are still loading AND no recents exist
	if v.anyLoading() && v.list.Len() == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading...")
	}
	return v.list.View()
}

// poolPending returns true if a pool has not yet resolved (no data, no error).
// A pool in error state is NOT pending — it resolved with a failure.
func poolPending[T any](snap data.Snapshot[T]) bool {
	return !snap.Usable() && snap.State != data.StateError
}

func (v *Home) anyLoading() bool {
	return poolPending(v.heyPool.Get()) || poolPending(v.assignPool.Get()) || poolPending(v.projectPool.Get())
}

// syncRecents populates recentItems from the local recents store.
func (v *Home) syncRecents() {
	store := v.session.Recents()
	if store == nil {
		v.recentItems = nil
		return
	}

	// Get recents across all accounts (empty filter)
	projects := store.Get(recents.TypeProject, "", "")
	recordings := store.Get(recents.TypeRecording, "", "")

	// Cap each at 5
	if len(projects) > 5 {
		projects = projects[:5]
	}
	if len(recordings) > 5 {
		recordings = recordings[:5]
	}

	accounts := sessionAccounts(v.session)
	var items []widget.ListItem

	for _, p := range projects {
		id := "recent:project:" + p.ID
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       p.Title,
			Description: p.Description,
			Extra:       accountExtra(accounts, p.AccountID, "project"),
		})

		pid, _ := strconv.ParseInt(p.ID, 10, 64)
		v.itemMeta[id] = homeItemMeta{
			accountID:  p.AccountID,
			projectID:  pid,
			viewTarget: workspace.ViewDock,
		}
	}

	for _, r := range recordings {
		id := "recent:recording:" + r.ID
		desc := r.Description
		if desc == "" && r.Type != "" {
			desc = r.Type
		}
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       r.Title,
			Description: desc,
			Extra:       accountExtra(accounts, r.AccountID, "recent"),
		})

		rid, _ := strconv.ParseInt(r.ID, 10, 64)
		pid, _ := strconv.ParseInt(r.ProjectID, 10, 64)

		target := workspace.ViewDetail
		if r.Description == campfireRecordingType {
			target = workspace.ViewCampfire
		}
		v.itemMeta[id] = homeItemMeta{
			accountID:   r.AccountID,
			projectID:   pid,
			recordingID: rid,
			recordType:  r.Description,
			viewTarget:  target,
		}
	}

	v.recentItems = items
}

// syncHeyEntries converts activity entries into heyItems (max 8).
func (v *Home) syncHeyEntries(entries []workspace.ActivityEntryInfo) {
	if len(entries) > 8 {
		entries = entries[:8]
	}

	accounts := sessionAccounts(v.session)
	items := make([]widget.ListItem, 0, len(entries))
	for _, e := range entries {
		id := fmt.Sprintf("hey:%s:%d", e.AccountID, e.ID)
		desc := e.Account
		if e.Project != "" {
			desc += " > " + e.Project
		}
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       e.Title,
			Description: desc,
			Extra:       accountExtra(accounts, e.AccountID, e.Type),
		})
		v.itemMeta[id] = homeItemMeta{
			accountID:   e.AccountID,
			projectID:   e.ProjectID,
			recordingID: e.ID,
			recordType:  e.Type,
			viewTarget:  workspace.ViewDetail,
		}
	}
	v.heyItems = items
}

// syncAssignments converts assignment data into assignItems (max 5).
func (v *Home) syncAssignments(assignments []workspace.AssignmentInfo) {
	// Filter out completed
	var active []workspace.AssignmentInfo
	for _, a := range assignments {
		if !a.Completed {
			active = append(active, a)
		}
	}
	if len(active) > 5 {
		active = active[:5]
	}

	accounts := sessionAccounts(v.session)
	items := make([]widget.ListItem, 0, len(active))
	for _, a := range active {
		id := fmt.Sprintf("assign:%s:%d", a.AccountID, a.ID)
		desc := a.Account
		if a.Project != "" {
			desc += " > " + a.Project
		}
		extra := accountExtra(accounts, a.AccountID, a.DueOn)
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       a.Content,
			Description: desc,
			Extra:       extra,
			Marked:      a.Overdue,
		})
		v.itemMeta[id] = homeItemMeta{
			accountID:   a.AccountID,
			projectID:   a.ProjectID,
			recordingID: a.ID,
			recordType:  "Todo",
			viewTarget:  workspace.ViewDetail,
		}
	}
	v.assignItems = items
}

// syncBookmarks filters projects to bookmarked ones and builds bookmarkItems.
func (v *Home) syncBookmarks(projects []data.ProjectInfo) {
	var items []widget.ListItem
	for _, p := range projects {
		if !p.Bookmarked {
			continue
		}
		id := fmt.Sprintf("bookmark:%s:%d", p.AccountID, p.ID)
		desc := p.Purpose
		if desc == "" {
			desc = p.Description
		}
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       p.Name,
			Description: desc,
			Marked:      true,
		})
		v.itemMeta[id] = homeItemMeta{
			accountID:  p.AccountID,
			projectID:  p.ID,
			viewTarget: workspace.ViewDock,
		}
	}
	v.bookmarkItems = items
}

// rebuildList combines all sections into a single list.
func (v *Home) rebuildList() {
	var items []widget.ListItem

	if len(v.recentItems) > 0 {
		items = append(items, widget.ListItem{Title: "RECENTS", Header: true})
		items = append(items, v.recentItems...)
	}
	if len(v.heyItems) > 0 {
		items = append(items, widget.ListItem{Title: "HEY!", Header: true})
		items = append(items, v.heyItems...)
	}
	if len(v.assignItems) > 0 {
		items = append(items, widget.ListItem{Title: "ASSIGNMENTS", Header: true})
		items = append(items, v.assignItems...)
	}
	if len(v.bookmarkItems) > 0 {
		items = append(items, widget.ListItem{Title: "BOOKMARKS", Header: true})
		items = append(items, v.bookmarkItems...)
	}

	v.list.SetItems(items)
}

// openSelected navigates to the selected item based on its ID prefix.
func (v *Home) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	// Section headers navigate to their corresponding full view
	if item.Header {
		switch item.Title {
		case "RECENTS":
			return workspace.Navigate(workspace.ViewMyStuff, v.session.Scope())
		case "HEY!":
			return workspace.Navigate(workspace.ViewHey, v.session.Scope())
		case "ASSIGNMENTS":
			return workspace.Navigate(workspace.ViewAssignments, v.session.Scope())
		case "BOOKMARKS":
			return workspace.Navigate(workspace.ViewProjects, v.session.Scope())
		}
		return nil
	}

	meta, ok := v.itemMeta[item.ID]
	if !ok {
		return nil
	}

	scope := v.session.Scope()
	if meta.accountID != "" {
		scope.AccountID = meta.accountID
	}
	if meta.projectID != 0 {
		scope.ProjectID = meta.projectID
	}

	switch meta.viewTarget {
	case workspace.ViewDock:
		scope.ProjectName = item.Title
		// Record project in recents
		if r := v.session.Recents(); r != nil {
			r.Add(recents.Item{
				ID:        fmt.Sprintf("%d", meta.projectID),
				Title:     item.Title,
				Type:      recents.TypeProject,
				AccountID: meta.accountID,
			})
		}
		return workspace.Navigate(workspace.ViewDock, scope)

	case workspace.ViewCampfire:
		scope.ToolType = "chat"
		scope.ToolID = meta.recordingID
		return workspace.Navigate(workspace.ViewCampfire, scope)

	case workspace.ViewDetail:
		scope.RecordingID = meta.recordingID
		scope.RecordingType = meta.recordType
		// Record in recents
		if r := v.session.Recents(); r != nil {
			r.Add(recents.Item{
				ID:          fmt.Sprintf("%d", meta.recordingID),
				Title:       item.Title,
				Description: meta.recordType,
				Type:        recents.TypeRecording,
				AccountID:   meta.accountID,
				ProjectID:   fmt.Sprintf("%d", meta.projectID),
			})
		}
		return workspace.Navigate(workspace.ViewDetail, scope)
	}

	return nil
}
