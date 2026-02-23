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

// Activity is the timeline feed view showing rich activity events across
// all accounts. Uses the Timeline.Progress() API for action-level detail.
type Activity struct {
	session *workspace.Session
	pool    *data.Pool[[]data.TimelineEventInfo]
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	// Entries metadata for navigation
	entryMeta map[string]workspace.TimelineEventInfo

	width, height int
}

// NewActivity creates the Activity timeline feed view.
func NewActivity(session *workspace.Session) *Activity {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoRecordings("activity"))
	list.SetFocused(true)

	pool := session.Hub().Timeline()

	return &Activity{
		session:   session,
		pool:      pool,
		styles:    styles,
		list:      list,
		spinner:   s,
		loading:   true,
		entryMeta: make(map[string]workspace.TimelineEventInfo),
	}
}

func (v *Activity) Title() string { return "Activity" }

func (v *Activity) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

func (v *Activity) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Activity) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Activity) InputActive() bool { return v.list.Filtering() }

func (v *Activity) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Activity) Init() tea.Cmd {
	cmds := []tea.Cmd{v.spinner.Tick}
	snap := v.pool.Get()
	if snap.Usable() {
		v.syncEntries(snap.Data)
		v.loading = false
	}
	if !snap.Fresh() {
		cmds = append(cmds, v.pool.FetchIfStale(v.session.Hub().Global().Context()))
	}
	cmds = append(cmds, v.schedulePoll())
	return tea.Batch(cmds...)
}

func (v *Activity) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
				return v, workspace.ReportError(snap.Err, "loading timeline")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().Global().Context()))

	case data.PollMsg:
		if msg.Tag == v.pool.Key() {
			return v, tea.Batch(
				v.pool.FetchIfStale(v.session.Hub().Global().Context()),
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

func (v *Activity) View() string {
	if v.loading && v.list.Len() == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading timeline...")
	}
	return v.list.View()
}

func (v *Activity) syncEntries(entries []workspace.TimelineEventInfo) {
	accounts := sessionAccounts(v.session)
	v.entryMeta = syncTimelineEntries(entries, v.list, accounts)
}

func (v *Activity) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	meta, ok := v.entryMeta[item.ID]
	if !ok {
		return nil
	}

	// Some timeline events (e.g., project-level) have no parent recording.
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
	scope.OriginView = "Activity"
	scope.OriginHint = meta.Action + " " + meta.Target
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Activity) schedulePoll() tea.Cmd {
	interval := v.pool.PollInterval()
	if interval == 0 {
		return nil
	}
	key := v.pool.Key()
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: key}
	})
}
