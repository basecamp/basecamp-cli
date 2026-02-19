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

// Schedule is the list view for a project's schedule entries.
type Schedule struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ScheduleEntryInfo]
	styles  *tui.Styles

	// Layout
	list          *widget.List
	spinner       spinner.Model
	loading       bool
	width, height int

	// Data
	entries []data.ScheduleEntryInfo
}

// NewSchedule creates the schedule view.
func NewSchedule(session *workspace.Session) *Schedule {
	styles := session.Styles()
	scope := session.Scope()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No schedule entries found.")
	list.SetFocused(true)

	pool := session.Hub().ScheduleEntries(scope.ProjectID, scope.ToolID)

	return &Schedule{
		session: session,
		pool:    pool,
		styles:  styles,
		list:    list,
		spinner: s,
		loading: true,
	}
}

// Title implements View.
func (v *Schedule) Title() string {
	return "Schedule"
}

// ShortHelp implements View.
func (v *Schedule) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

// FullHelp implements View.
func (v *Schedule) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Schedule) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Schedule) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *Schedule) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

// Init implements tea.Model.
func (v *Schedule) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.entries = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Context()))
}

// Update implements tea.Model.
func (v *Schedule) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.entries = snap.Data
				v.syncList()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading schedule entries")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Context()))

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
		return v, v.handleKey(msg)
	}
	return v, nil
}

func (v *Schedule) handleKey(msg tea.KeyMsg) tea.Cmd {
	keys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, keys.Open):
		return v.openSelectedEntry()
	default:
		return v.list.Update(msg)
	}
}

func (v *Schedule) openSelectedEntry() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var entryID int64
	fmt.Sscanf(item.ID, "%d", &entryID)

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Schedule::Entry",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = entryID
	scope.RecordingType = "Schedule::Entry"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

// View implements tea.Model.
func (v *Schedule) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading schedule...")
	}

	return v.list.View()
}

// -- Data sync

func (v *Schedule) syncList() {
	items := make([]widget.ListItem, 0, len(v.entries))
	for _, e := range v.entries {
		title := e.Summary
		if title == "" {
			title = "(untitled)"
		}

		desc := e.StartsAt
		if e.EndsAt != "" && e.EndsAt != e.StartsAt {
			desc += " - " + e.EndsAt
		}
		if len(e.Participants) > 0 {
			if desc != "" {
				desc += "  "
			}
			desc += strings.Join(e.Participants, ", ")
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", e.ID),
			Title:       title,
			Description: desc,
		})
	}
	v.list.SetItems(items)
}

