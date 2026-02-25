package views

import (
	"fmt"
	"time"

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

// Assignments-local mutation result messages.
type assignmentCompleteResultMsg struct {
	itemID string
	err    error
}
type assignmentTrashResultMsg struct {
	itemID string
	err    error
}
type assignmentTrashTimeoutMsg struct{}

// Assignments shows cross-account todo assignments for the current user,
// grouped by due date (overdue, this week, later).
type Assignments struct {
	session *workspace.Session
	pool    *data.Pool[[]data.AssignmentInfo]
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	assignmentMeta map[string]workspace.AssignmentInfo
	excluded       map[string]bool // items completed/trashed, pending pool refresh

	// Double-press trash confirmation
	trashPending   bool
	trashPendingID string

	width, height int
}

// NewAssignments creates the cross-account assignments view.
func NewAssignments(session *workspace.Session) *Assignments {
	styles := session.Styles()
	pool := session.Hub().Assignments()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoAssignments())
	list.SetFocused(true)

	return &Assignments{
		session:        session,
		pool:           pool,
		styles:         styles,
		list:           list,
		spinner:        s,
		loading:        true,
		assignmentMeta: make(map[string]workspace.AssignmentInfo),
		excluded:       make(map[string]bool),
	}
}

func (v *Assignments) Title() string { return "Assignments" }

// FocusedItem implements workspace.FocusedRecording.
func (v *Assignments) FocusedItem() workspace.FocusedItemScope {
	item := v.list.Selected()
	if item == nil {
		return workspace.FocusedItemScope{}
	}
	meta, ok := v.assignmentMeta[item.ID]
	if !ok {
		return workspace.FocusedItemScope{}
	}
	return workspace.FocusedItemScope{
		AccountID:   meta.AccountID,
		ProjectID:   meta.ProjectID,
		RecordingID: meta.ID,
	}
}

func (v *Assignments) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "complete")),
		key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "boost")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trash")),
	}
}

func (v *Assignments) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Assignments) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Assignments) InputActive() bool { return v.list.Filtering() }

func (v *Assignments) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Assignments) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.syncAssignments(snap.Data)
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().Global().Context()))
}

func (v *Assignments) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.State == data.StateFresh {
				v.excluded = make(map[string]bool) // API response reflects mutations
			}
			if snap.Usable() {
				v.syncAssignments(snap.Data)
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading assignments")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.FocusMsg:
		return v, v.pool.FetchIfStale(v.session.Hub().Global().Context())

	case assignmentCompleteResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "completing todo")
		}
		v.excluded[msg.itemID] = true
		snap := v.pool.Get()
		if snap.Usable() {
			v.syncAssignments(snap.Data)
		}
		return v, workspace.SetStatus("Completed", false)

	case assignmentTrashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing todo")
		}
		v.excluded[msg.itemID] = true
		snap := v.pool.Get()
		if snap.Usable() {
			v.syncAssignments(snap.Data)
		}
		return v, workspace.SetStatus("Trashed", false)

	case assignmentTrashTimeoutMsg:
		v.trashPending = false
		v.trashPendingID = ""
		return v, nil

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

		// Reset trash confirmation on non-t keys or when filtering
		if msg.String() != "t" || v.list.Filtering() {
			v.trashPending = false
			v.trashPendingID = ""
		}

		// Mutation keys: blocked during filter
		if !v.list.Filtering() {
			switch msg.String() {
			case "x":
				return v, v.completeSelected()
			case "b", "B":
				return v, v.boostSelected()
			case "t":
				return v, v.trashSelected()
			}
		}

		keys := workspace.DefaultListKeyMap()
		switch {
		case key.Matches(msg, keys.Open):
			return v, v.openSelected()
		default:
			return v, v.list.Update(msg)
		}
	}
	return v, nil
}

func (v *Assignments) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading assignments...")
	}
	return v.list.View()
}

func (v *Assignments) syncAssignments(assignments []workspace.AssignmentInfo) {
	v.assignmentMeta = make(map[string]workspace.AssignmentInfo, len(assignments))
	var items []widget.ListItem
	accounts := sessionAccounts(v.session)
	now := time.Now()
	weekEnd := now.AddDate(0, 0, 7-int(now.Weekday()))

	var overdue, thisWeek, later, noDue []workspace.AssignmentInfo
	for _, a := range assignments {
		if a.Completed {
			continue
		}
		if a.DueOn == "" {
			noDue = append(noDue, a)
			continue
		}
		due, err := time.Parse("2006-01-02", a.DueOn)
		if err != nil {
			noDue = append(noDue, a)
			continue
		}
		switch {
		case due.Before(now.Truncate(24 * time.Hour)):
			a.Overdue = true
			overdue = append(overdue, a)
		case due.Before(weekEnd):
			thisWeek = append(thisWeek, a)
		default:
			later = append(later, a)
		}
	}

	addGroup := func(label string, group []workspace.AssignmentInfo) {
		if len(group) == 0 {
			return
		}
		items = append(items, widget.ListItem{Title: label, Header: true})
		for _, a := range group {
			id := fmt.Sprintf("%s:%d", a.AccountID, a.ID)
			if v.excluded[id] {
				continue
			}
			v.assignmentMeta[id] = a
			desc := a.Account
			if a.Project != "" {
				desc += " > " + a.Project
			}
			if a.Todolist != "" {
				desc += " · " + a.Todolist
			}
			extra := accountExtra(accounts, a.AccountID, a.DueOn)
			items = append(items, widget.ListItem{
				ID:          id,
				Title:       a.Content,
				Description: desc,
				Extra:       extra,
				Marked:      a.Overdue,
			})
		}
	}

	addGroup("Overdue", overdue)
	addGroup("This Week", thisWeek)
	addGroup("Later", later)
	addGroup("No Due Date", noDue)

	v.list.SetItems(items)
}

func (v *Assignments) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	meta, ok := v.assignmentMeta[item.ID]
	if !ok {
		return nil
	}

	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       meta.Content,
			Description: "Todo",
			Type:        recents.TypeRecording,
			AccountID:   meta.AccountID,
			ProjectID:   fmt.Sprintf("%d", meta.ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.AccountID = meta.AccountID
	scope.ProjectID = meta.ProjectID
	scope.RecordingID = meta.ID
	scope.RecordingType = "Todo"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Assignments) completeSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	meta, ok := v.assignmentMeta[item.ID]
	if !ok {
		return nil
	}

	hub := v.session.Hub()
	ctx := hub.Global().Context()
	itemID := item.ID
	return func() tea.Msg {
		err := hub.CompleteTodo(ctx, meta.AccountID, meta.ProjectID, meta.ID)
		return assignmentCompleteResultMsg{itemID: itemID, err: err}
	}
}

func (v *Assignments) trashSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	meta, ok := v.assignmentMeta[item.ID]
	if !ok {
		return nil
	}

	if v.trashPending && v.trashPendingID == item.ID {
		v.trashPending = false
		v.trashPendingID = ""
		hub := v.session.Hub()
		ctx := hub.Global().Context()
		itemID := item.ID
		return func() tea.Msg {
			err := hub.TrashRecording(ctx, meta.AccountID, meta.ProjectID, meta.ID)
			return assignmentTrashResultMsg{itemID: itemID, err: err}
		}
	}
	v.trashPending = true
	v.trashPendingID = item.ID
	return tea.Batch(
		workspace.SetStatus("Press t again to trash", false),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return assignmentTrashTimeoutMsg{} }),
	)
}

func (v *Assignments) boostSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	meta, ok := v.assignmentMeta[item.ID]
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return workspace.OpenBoostPickerMsg{
			Target: workspace.BoostTarget{
				ProjectID:   meta.ProjectID,
				RecordingID: meta.ID,
				AccountID:   meta.AccountID,
				Title:       meta.Content,
			},
		}
	}
}
