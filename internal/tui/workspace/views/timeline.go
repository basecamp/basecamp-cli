package views

import (
	"fmt"
	"time"

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

// Timeline is a project-scoped timeline view showing activity events
// for a single project. Structurally similar to Activity but uses the
// project-realm pool and project context.
type Timeline struct {
	session   *workspace.Session
	pool      *data.Pool[[]data.TimelineEventInfo]
	projectID int64
	styles    *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	entryMeta map[string]workspace.TimelineEventInfo

	width, height int
}

// NewTimeline creates a project-scoped timeline view.
func NewTimeline(session *workspace.Session, projectID int64) *Timeline {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity for this project.")
	list.SetFocused(true)

	pool := session.Hub().ProjectTimeline(projectID)

	return &Timeline{
		session:   session,
		pool:      pool,
		projectID: projectID,
		styles:    styles,
		list:      list,
		spinner:   s,
		loading:   true,
		entryMeta: make(map[string]workspace.TimelineEventInfo),
	}
}

func (v *Timeline) Title() string { return "Project Activity" }

func (v *Timeline) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

func (v *Timeline) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Timeline) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Timeline) InputActive() bool { return v.list.Filtering() }

func (v *Timeline) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Timeline) Init() tea.Cmd {
	cmds := []tea.Cmd{v.spinner.Tick}
	snap := v.pool.Get()
	if snap.Usable() {
		v.syncEntries(snap.Data)
		v.loading = false
	}
	if !snap.Fresh() {
		cmds = append(cmds, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
	}
	cmds = append(cmds, v.schedulePoll())
	return tea.Batch(cmds...)
}

func (v *Timeline) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				hadEntries := v.list.Len() > 0
				v.syncEntries(snap.Data)
				v.loading = false
				if hadEntries {
					v.pool.RecordHit()
				} else {
					v.pool.RecordMiss()
				}
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading project timeline")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

	case data.PollMsg:
		if msg.Tag == v.pool.Key() {
			return v, tea.Batch(
				v.pool.FetchIfStale(v.session.Hub().ProjectContext()),
				v.schedulePoll(),
			)
		}

	case workspace.FocusMsg:
		v.pool.SetFocused(true)

	case workspace.BlurMsg:
		v.pool.SetFocused(false)

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

func (v *Timeline) View() string {
	if v.loading && v.list.Len() == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading project timeline...")
	}
	return v.list.View()
}

func (v *Timeline) syncEntries(entries []workspace.TimelineEventInfo) {
	// Project-scoped: no account badges needed
	v.entryMeta = syncTimelineEntries(entries, v.list, nil)
}

func (v *Timeline) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	meta, ok := v.entryMeta[item.ID]
	if !ok {
		return nil
	}

	if meta.RecordingID <= 0 {
		return workspace.SetStatus("Cannot open this event type", false)
	}

	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          fmt.Sprintf("%d", meta.RecordingID),
			Title:       meta.Title,
			Description: meta.Target,
			Type:        recents.TypeRecording,
			AccountID:   meta.AccountID,
			ProjectID:   fmt.Sprintf("%d", meta.ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.AccountID = meta.AccountID
	scope.ProjectID = meta.ProjectID
	scope.RecordingID = meta.RecordingID
	scope.RecordingType = meta.Target
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Timeline) schedulePoll() tea.Cmd {
	interval := v.pool.PollInterval()
	if interval == 0 {
		return nil
	}
	key := v.pool.Key()
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: key}
	})
}
