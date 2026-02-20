package views

import (
	"fmt"

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

// Pulse shows recent activity across all accounts — a unified timeline of
// recently updated recordings grouped by time bucket.
type Pulse struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ActivityEntryInfo]
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	entryMeta map[string]workspace.ActivityEntryInfo

	width, height int
}

// NewPulse creates the activity pulse view.
func NewPulse(session *workspace.Session) *Pulse {
	styles := session.Styles()
	pool := session.Hub().Pulse()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No recent activity across accounts.")
	list.SetFocused(true)

	return &Pulse{
		session:   session,
		pool:      pool,
		styles:    styles,
		list:      list,
		spinner:   s,
		loading:   true,
		entryMeta: make(map[string]workspace.ActivityEntryInfo),
	}
}

func (v *Pulse) Title() string { return "Pulse" }

func (v *Pulse) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

func (v *Pulse) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Pulse) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Pulse) InputActive() bool { return v.list.Filtering() }

func (v *Pulse) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Pulse) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.syncEntries(snap.Data)
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().Global().Context()))
}

func (v *Pulse) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.syncEntries(snap.Data)
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading pulse")
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

func (v *Pulse) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading activity pulse...")
	}
	return v.list.View()
}

func (v *Pulse) syncEntries(entries []workspace.ActivityEntryInfo) {
	// Reuse the same time-bucket grouping as Hey
	v.entryMeta = make(map[string]workspace.ActivityEntryInfo, len(entries))
	accounts := sessionAccounts(v.session)
	var items []widget.ListItem

	// Group by account
	byAccount := make(map[string][]workspace.ActivityEntryInfo)
	var accountOrder []string
	for _, e := range entries {
		if _, seen := byAccount[e.AccountID]; !seen {
			accountOrder = append(accountOrder, e.AccountID)
		}
		byAccount[e.AccountID] = append(byAccount[e.AccountID], e)
	}

	for _, acctID := range accountOrder {
		group := byAccount[acctID]
		if len(group) == 0 {
			continue
		}
		acctName := group[0].Account
		if len(accountOrder) > 1 {
			items = append(items, widget.ListItem{Title: acctName, Header: true})
		}
		for _, e := range group {
			id := fmt.Sprintf("%s:%d", e.AccountID, e.ID)
			v.entryMeta[id] = e
			desc := e.Project
			if e.Creator != "" {
				desc = e.Creator + " > " + desc
			}
			items = append(items, widget.ListItem{
				ID:          id,
				Title:       e.Title,
				Description: desc,
				Extra:       accountExtra(accounts, e.AccountID, e.UpdatedAt),
			})
		}
	}

	v.list.SetItems(items)
}

func (v *Pulse) openSelected() tea.Cmd {
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
			ID:          item.ID,
			Title:       meta.Title,
			Description: meta.Type,
			Type:        recents.TypeRecording,
			AccountID:   meta.AccountID,
			ProjectID:   fmt.Sprintf("%d", meta.ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.AccountID = meta.AccountID
	scope.ProjectID = meta.ProjectID
	scope.RecordingID = meta.ID
	scope.RecordingType = meta.Type
	return workspace.Navigate(workspace.ViewDetail, scope)
}
