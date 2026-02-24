package views

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Pings shows 1:1 campfire threads across all accounts.
// Discovery: list all campfires per account, identify 1:1 rooms,
// fetch the latest line from each.
type Pings struct {
	session *workspace.Session
	pool    *data.Pool[[]data.PingRoomInfo]
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	roomMeta map[string]workspace.PingRoomInfo

	width, height int
}

// NewPings creates the pings view.
func NewPings(session *workspace.Session) *Pings {
	styles := session.Styles()
	pool := session.Hub().PingRooms()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoPings())
	list.SetFocused(true)

	return &Pings{
		session:  session,
		pool:     pool,
		styles:   styles,
		list:     list,
		spinner:  s,
		loading:  true,
		roomMeta: make(map[string]workspace.PingRoomInfo),
	}
}

func (v *Pings) Title() string { return "Pings" }

func (v *Pings) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open thread")),
	}
}

func (v *Pings) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Pings) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Pings) InputActive() bool { return v.list.Filtering() }

func (v *Pings) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Pings) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.syncRooms(snap.Data)
		v.loading = false
		if snap.Fresh() {
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().Global().Context()))
}

func (v *Pings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.syncRooms(snap.Data)
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading pings")
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

func (v *Pings) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Discovering ping threads...")
	}
	return v.list.View()
}

func (v *Pings) syncRooms(rooms []workspace.PingRoomInfo) {
	v.roomMeta = make(map[string]workspace.PingRoomInfo, len(rooms))
	accounts := sessionAccounts(v.session)
	var items []widget.ListItem

	// Group by account if multiple
	byAccount := make(map[string][]workspace.PingRoomInfo)
	var accountOrder []string
	for _, r := range rooms {
		if _, seen := byAccount[r.AccountID]; !seen {
			accountOrder = append(accountOrder, r.AccountID)
		}
		byAccount[r.AccountID] = append(byAccount[r.AccountID], r)
	}

	for _, acctID := range accountOrder {
		group := byAccount[acctID]
		if len(group) == 0 {
			continue
		}
		if len(accountOrder) > 1 {
			items = append(items, widget.ListItem{Title: group[0].Account, Header: true})
		}
		for _, r := range group {
			id := fmt.Sprintf("%s:%d-%d", r.AccountID, r.ProjectID, r.CampfireID)
			v.roomMeta[id] = r
			items = append(items, widget.ListItem{
				ID:          id,
				Title:       r.PersonName,
				Description: r.LastMessage,
				Extra:       accountExtra(accounts, r.AccountID, r.LastAt),
			})
		}
	}

	v.list.SetItems(items)
}

func (v *Pings) openSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}

	meta, ok := v.roomMeta[item.ID]
	if !ok {
		return nil
	}

	// Navigate to campfire view for this 1:1 room
	scope := v.session.Scope()
	scope.AccountID = meta.AccountID
	scope.ProjectID = meta.ProjectID
	scope.ToolType = "chat"
	scope.ToolID = meta.CampfireID
	return workspace.Navigate(workspace.ViewCampfire, scope)
}
