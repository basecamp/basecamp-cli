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
	list.SetEmptyText("No recent activity.")
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
	v.entryMeta = make(map[string]workspace.TimelineEventInfo, len(entries))
	items := make([]widget.ListItem, 0, len(entries)+4) // room for time headers

	// Group by time bucket
	now := time.Now()
	var justNow, hourAgo, today, yesterday, older []workspace.TimelineEventInfo

	for _, e := range entries {
		age := now.Unix() - e.CreatedAtTS
		switch {
		case age < 600: // 10 min
			justNow = append(justNow, e)
		case age < 3600: // 1 hour
			hourAgo = append(hourAgo, e)
		case age < 86400 && now.Day() == time.Unix(e.CreatedAtTS, 0).Day():
			today = append(today, e)
		case age < 172800:
			yesterday = append(yesterday, e)
		default:
			older = append(older, e)
		}
	}

	addGroup := func(label string, group []workspace.TimelineEventInfo) {
		if len(group) == 0 {
			return
		}
		items = append(items, widget.ListItem{Title: label, Header: true})
		for _, e := range group {
			id := fmt.Sprintf("%d", e.RecordingID)
			v.entryMeta[id] = e

			// Title: "Action Target: Title" e.g. "completed Todo: Ship feature"
			title := e.Action + " " + e.Target
			if e.Title != "" {
				title += ": " + e.Title
			}

			// Description: "Creator · Project · Time"
			desc := e.Creator
			if e.Project != "" {
				desc += " · " + e.Project
			}
			desc += " · " + e.CreatedAt

			extra := ""
			if e.SummaryExcerpt != "" {
				extra = e.SummaryExcerpt
				if len(extra) > 50 {
					extra = extra[:47] + "..."
				}
			}

			items = append(items, widget.ListItem{
				ID:          id,
				Title:       title,
				Description: desc,
				Extra:       extra,
			})
		}
	}

	addGroup("JUST NOW", justNow)
	addGroup("1 HOUR AGO", hourAgo)
	addGroup("TODAY", today)
	addGroup("YESTERDAY", yesterday)
	addGroup("OLDER", older)

	v.list.SetItems(items)
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
