package views

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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

// Home is a combined dashboard view that shows recents, activity, assignments,
// and bookmarked projects in a single scrollable list with section headers.
// It renders instantly with local recents data, then progressively fills in
// API-sourced sections.
type Home struct {
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model

	// Section data (populated progressively)
	recentItems   []widget.ListItem // instant from recents store
	heyItems      []widget.ListItem // from API
	assignItems   []widget.ListItem // from API
	bookmarkItems []widget.ListItem // from API

	// Loading state
	heyLoaded       bool
	assignLoaded    bool
	bookmarksLoaded bool

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
func NewHome(session *workspace.Session, store *data.Store) *Home {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("Welcome to Basecamp.")
	list.SetFocused(true)

	v := &Home{
		session:  session,
		store:    store,
		styles:   styles,
		list:     list,
		spinner:  s,
		itemMeta: make(map[string]homeItemMeta),
	}

	v.syncRecents()

	return v
}

func (v *Home) Title() string { return "Home" }

func (v *Home) ShortHelp() []key.Binding {
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
	cmds := make([]tea.Cmd, 0, 4)
	cmds = append(cmds, v.spinner.Tick)

	// Recents are already synced in NewHome (instant, no API).
	// Fire parallel API fetches for the remaining sections.
	cmds = append(cmds, v.fetchHeyActivity(), v.fetchAssignments(), v.fetchBookmarks())

	return tea.Batch(cmds...)
}

func (v *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.HomeHeyLoadedMsg:
		v.heyLoaded = true
		if msg.Err == nil {
			v.syncHeyEntries(msg.Entries)
		}
		v.rebuildList()
		return v, nil

	case workspace.HomeAssignmentsLoadedMsg:
		v.assignLoaded = true
		if msg.Err == nil {
			v.syncAssignments(msg.Assignments)
		}
		v.rebuildList()
		return v, nil

	case workspace.HomeProjectsLoadedMsg:
		v.bookmarksLoaded = true
		if msg.Err == nil {
			v.syncBookmarks(msg.Projects)
		}
		v.rebuildList()
		return v, nil

	case workspace.RefreshMsg:
		v.heyLoaded = false
		v.assignLoaded = false
		v.bookmarksLoaded = false
		v.syncRecents()
		v.rebuildList()
		return v, tea.Batch(v.spinner.Tick, v.fetchHeyActivity(), v.fetchAssignments(), v.fetchBookmarks())

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

func (v *Home) anyLoading() bool {
	return !v.heyLoaded || !v.assignLoaded || !v.bookmarksLoaded
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

	var items []widget.ListItem

	for _, p := range projects {
		id := "recent:project:" + p.ID
		items = append(items, widget.ListItem{
			ID:          id,
			Title:       p.Title,
			Description: p.Description,
			Extra:       "project",
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
			Extra:       "recent",
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
			Extra:       e.Type,
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

	items := make([]widget.ListItem, 0, len(active))
	for _, a := range active {
		id := fmt.Sprintf("assign:%s:%d", a.AccountID, a.ID)
		desc := a.Account
		if a.Project != "" {
			desc += " > " + a.Project
		}
		extra := a.DueOn
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
func (v *Home) syncBookmarks(projects []basecamp.Project) {
	var items []widget.ListItem
	for _, p := range projects {
		if !p.Bookmarked {
			continue
		}
		id := fmt.Sprintf("bookmark:%d", p.ID)
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

// Fetch functions — slim versions of existing patterns for home dashboard.
// Each uses stale-while-revalidate: serve cached data immediately, refresh
// in background when stale.

// fetchHeyActivity serves cached activity if available, revalidating when stale.
func (v *Home) fetchHeyActivity() tea.Cmd {
	cache := v.session.MultiStore().Cache()
	entry := cache.Get("home:hey")

	if entry != nil {
		entries, _ := entry.Value.([]workspace.ActivityEntryInfo) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.HomeHeyLoadedMsg{Entries: entries}
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return workspace.HomeHeyLoadedMsg{Entries: entries}
			},
			v.fetchHeyActivityFromAPI(),
		)
	}

	return v.fetchHeyActivityFromAPI()
}

// fetchHeyActivityFromAPI fans out across all accounts for recent recordings.
func (v *Home) fetchHeyActivityFromAPI() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	scope := v.session.Scope()
	return func() tea.Msg {
		accounts := ms.Accounts()
		if len(accounts) == 0 {
			if scope.AccountID == "" {
				return workspace.HomeHeyLoadedMsg{}
			}
			return v.fetchSingleAccountHey(ctx, client, scope)
		}

		recordingTypes := []basecamp.RecordingType{
			basecamp.RecordingTypeMessage,
			basecamp.RecordingTypeTodo,
			basecamp.RecordingTypeDocument,
		}

		var allEntries []workspace.ActivityEntryInfo
		results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
			var entries []workspace.ActivityEntryInfo
			for _, rt := range recordingTypes {
				result, err := client.Recordings().List(ctx, rt, &basecamp.RecordingsListOptions{
					Sort:      "updated_at",
					Direction: "desc",
					Limit:     3,
					Page:      1,
				})
				if err != nil {
					continue
				}
				for _, rec := range result.Recordings {
					entries = append(entries, recordingToEntry(rec, acct))
				}
			}
			return entries, nil
		})

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if entries, ok := r.Data.([]workspace.ActivityEntryInfo); ok {
				allEntries = append(allEntries, entries...)
			}
		}

		sort.Slice(allEntries, func(i, j int) bool {
			return allEntries[i].UpdatedAtTS > allEntries[j].UpdatedAtTS
		})
		if len(allEntries) > 8 {
			allEntries = allEntries[:8]
		}

		ms.Cache().Set("home:hey", allEntries, 30*time.Second, 5*time.Minute)
		// Warm the Hey! view cache so navigating there is instant.
		ms.Cache().Set("hey:activity", allEntries, 15*time.Second, 2*time.Minute)
		return workspace.HomeHeyLoadedMsg{Entries: allEntries}
	}
}

func (v *Home) fetchSingleAccountHey(ctx context.Context, client *basecamp.AccountClient, scope workspace.Scope) workspace.HomeHeyLoadedMsg {
	var entries []workspace.ActivityEntryInfo
	for _, rt := range []basecamp.RecordingType{
		basecamp.RecordingTypeMessage,
		basecamp.RecordingTypeTodo,
		basecamp.RecordingTypeDocument,
	} {
		result, err := client.Recordings().List(ctx, rt, &basecamp.RecordingsListOptions{
			Sort:      "updated_at",
			Direction: "desc",
			Limit:     3,
			Page:      1,
		})
		if err != nil {
			continue
		}
		acct := data.AccountInfo{ID: scope.AccountID, Name: scope.AccountName}
		for _, rec := range result.Recordings {
			entries = append(entries, recordingToEntry(rec, acct))
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].UpdatedAtTS > entries[j].UpdatedAtTS
	})
	if len(entries) > 8 {
		entries = entries[:8]
	}

	return workspace.HomeHeyLoadedMsg{Entries: entries}
}

// fetchAssignments serves cached assignments if available, revalidating when stale.
func (v *Home) fetchAssignments() tea.Cmd {
	cache := v.session.MultiStore().Cache()
	entry := cache.Get("home:assignments")

	if entry != nil {
		assignments, _ := entry.Value.([]workspace.AssignmentInfo) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.HomeAssignmentsLoadedMsg{Assignments: assignments}
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return workspace.HomeAssignmentsLoadedMsg{Assignments: assignments}
			},
			v.fetchAssignmentsFromAPI(),
		)
	}

	return v.fetchAssignmentsFromAPI()
}

// fetchAssignmentsFromAPI fans out to get active todos across all accounts.
func (v *Home) fetchAssignmentsFromAPI() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	scope := v.session.Scope()
	return func() tea.Msg {

		identity := ms.Identity()
		if identity == nil {
			return workspace.HomeAssignmentsLoadedMsg{
				Err: fmt.Errorf("identity not available yet"),
			}
		}
		myName := identity.FirstName + " " + identity.LastName

		accounts := ms.Accounts()
		if len(accounts) == 0 {
			if scope.AccountID == "" {
				return workspace.HomeAssignmentsLoadedMsg{}
			}
			assignments := fetchAccountAssignments(ctx, client,
				data.AccountInfo{ID: scope.AccountID, Name: scope.AccountName},
				myName)
			if len(assignments) > 5 {
				assignments = assignments[:5]
			}
			ms.Cache().Set("home:assignments", assignments, 30*time.Second, 5*time.Minute)
			ms.Cache().Set("assignments", assignments, 15*time.Second, 2*time.Minute)
			return workspace.HomeAssignmentsLoadedMsg{Assignments: assignments}
		}

		var allAssignments []workspace.AssignmentInfo
		results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
			return fetchAccountAssignments(ctx, client, acct, myName), nil
		})

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if assignments, ok := r.Data.([]workspace.AssignmentInfo); ok {
				allAssignments = append(allAssignments, assignments...)
			}
		}

		sort.Slice(allAssignments, func(i, j int) bool {
			if allAssignments[i].DueOn == "" {
				return false
			}
			if allAssignments[j].DueOn == "" {
				return true
			}
			return allAssignments[i].DueOn < allAssignments[j].DueOn
		})

		if len(allAssignments) > 15 {
			allAssignments = allAssignments[:15]
		}

		ms.Cache().Set("home:assignments", allAssignments, 30*time.Second, 5*time.Minute)
		// Warm the Assignments view cache so navigating there is instant.
		ms.Cache().Set("assignments", allAssignments, 15*time.Second, 2*time.Minute)
		return workspace.HomeAssignmentsLoadedMsg{Assignments: allAssignments}
	}
}

// fetchBookmarks serves cached bookmarks if available, revalidating when stale.
func (v *Home) fetchBookmarks() tea.Cmd {
	cache := v.session.MultiStore().Cache()
	entry := cache.Get("home:bookmarks")

	if entry != nil {
		projects, _ := entry.Value.([]basecamp.Project) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.HomeProjectsLoadedMsg{Projects: projects}
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return workspace.HomeProjectsLoadedMsg{Projects: projects}
			},
			v.fetchBookmarksFromAPI(),
		)
	}

	return v.fetchBookmarksFromAPI()
}

// fetchBookmarksFromAPI fans out to get all projects and filter to bookmarked ones.
func (v *Home) fetchBookmarksFromAPI() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	scope := v.session.Scope()
	return func() tea.Msg {
		accounts := ms.Accounts()

		if len(accounts) == 0 {
			if scope.AccountID == "" {
				return workspace.HomeProjectsLoadedMsg{}
			}
			result, err := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
			if err != nil {
				return workspace.HomeProjectsLoadedMsg{Err: err}
			}
			ms.Cache().Set("home:bookmarks", result.Projects, 30*time.Second, 5*time.Minute)
			ms.Cache().Set("projects:list", result.Projects, 15*time.Second, 2*time.Minute)
			return workspace.HomeProjectsLoadedMsg{Projects: result.Projects}
		}

		var allProjects []basecamp.Project
		results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
			result, err := client.Projects().List(ctx, &basecamp.ProjectListOptions{})
			if err != nil {
				return nil, err
			}
			return result.Projects, nil
		})

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if projects, ok := r.Data.([]basecamp.Project); ok {
				allProjects = append(allProjects, projects...)
			}
		}

		ms.Cache().Set("home:bookmarks", allProjects, 30*time.Second, 5*time.Minute)
		// Can't warm projects:multi (different type), but projects:list works for data overlap
		return workspace.HomeProjectsLoadedMsg{Projects: allProjects}
	}
}
