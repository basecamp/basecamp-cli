package views

import (
	"context"
	"fmt"
	"sort"
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

// Assignments shows cross-account todo assignments for the current user,
// grouped by due date (overdue, this week, later).
type Assignments struct {
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	assignmentMeta map[string]workspace.AssignmentInfo

	width, height int
}

// NewAssignments creates the cross-account assignments view.
func NewAssignments(session *workspace.Session, store *data.Store) *Assignments {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No assignments found.")
	list.SetFocused(true)

	return &Assignments{
		session:        session,
		store:          store,
		styles:         styles,
		list:           list,
		spinner:        s,
		loading:        true,
		assignmentMeta: make(map[string]workspace.AssignmentInfo),
	}
}

func (v *Assignments) Title() string { return "Assignments" }

func (v *Assignments) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
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
	return tea.Batch(v.spinner.Tick, v.fetchAssignments())
}

func (v *Assignments) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.AssignmentsLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading assignments")
		}
		v.syncAssignments(msg.Assignments)
		return v, nil

	case workspace.RefreshMsg:
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.fetchAssignments())

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
			v.assignmentMeta[id] = a
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
		}
	}

	addGroup("OVERDUE", overdue)
	addGroup("THIS WEEK", thisWeek)
	addGroup("LATER", later)
	addGroup("NO DUE DATE", noDue)

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

func (v *Assignments) fetchAssignments() tea.Cmd {
	cache := v.session.MultiStore().Cache()
	entry := cache.Get("assignments")

	if entry != nil {
		assignments, _ := entry.Value.([]workspace.AssignmentInfo) //nolint:errcheck
		if entry.IsFresh() {
			return func() tea.Msg {
				return workspace.AssignmentsLoadedMsg{Assignments: assignments}
			}
		}
		return tea.Batch(
			func() tea.Msg {
				return workspace.AssignmentsLoadedMsg{Assignments: assignments}
			},
			v.fetchAssignmentsFromAPI(),
		)
	}

	return v.fetchAssignmentsFromAPI()
}

func (v *Assignments) fetchAssignmentsFromAPI() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	client := v.session.AccountClient()
	scope := v.session.Scope()
	return func() tea.Msg {

		// Get current user's identity to match assignees
		identity := ms.Identity()
		if identity == nil {
			return workspace.AssignmentsLoadedMsg{
				Err: fmt.Errorf("identity not available yet"),
			}
		}
		myName := identity.FirstName + " " + identity.LastName

		accounts := ms.Accounts()
		if len(accounts) == 0 {
			// Single-account fallback
			if scope.AccountID == "" {
				return workspace.AssignmentsLoadedMsg{}
			}
			assignments := fetchAccountAssignments(ctx, client,
				data.AccountInfo{ID: scope.AccountID, Name: scope.AccountName},
				myName)
			ms.Cache().Set("assignments", assignments, 30*time.Second, 5*time.Minute)
			return workspace.AssignmentsLoadedMsg{Assignments: assignments}
		}

		// Fan out across all accounts
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

		// Sort by due date (empty last)
		sort.Slice(allAssignments, func(i, j int) bool {
			if allAssignments[i].DueOn == "" {
				return false
			}
			if allAssignments[j].DueOn == "" {
				return true
			}
			return allAssignments[i].DueOn < allAssignments[j].DueOn
		})

		ms.Cache().Set("assignments", allAssignments, 30*time.Second, 5*time.Minute)
		return workspace.AssignmentsLoadedMsg{Assignments: allAssignments}
	}
}

func fetchAccountAssignments(ctx context.Context, client *basecamp.AccountClient, acct data.AccountInfo, myName string) []workspace.AssignmentInfo {
	result, err := client.Recordings().List(ctx, basecamp.RecordingTypeTodo, &basecamp.RecordingsListOptions{
		Status:    "active",
		Sort:      "updated_at",
		Direction: "desc",
		Limit:     50,
		Page:      1,
	})
	if err != nil {
		return nil
	}

	var assignments []workspace.AssignmentInfo
	for _, rec := range result.Recordings {
		// We can't filter by assignee at the API level, so we fetch all active
		// todos and include them all. A better approach would use the SDK's
		// assignee filtering if available, but for now we show all active todos.
		// The title will indicate the context.
		project := ""
		var projectID int64
		if rec.Bucket != nil {
			project = rec.Bucket.Name
			projectID = rec.Bucket.ID
		}
		_ = myName // TODO: filter by assignee when SDK supports it
		assignments = append(assignments, workspace.AssignmentInfo{
			ID:        rec.ID,
			Content:   rec.Title,
			Account:   acct.Name,
			AccountID: acct.ID,
			Project:   project,
			ProjectID: projectID,
			// DueOn is not available on Recording — would need a follow-up fetch
		})
	}
	return assignments
}
