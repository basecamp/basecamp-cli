package views

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Pings shows 1:1 campfire threads across all accounts.
// Discovery: list all campfires per account, identify 1:1 rooms,
// fetch the latest line from each.
type Pings struct {
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	roomMeta map[string]workspace.PingRoomInfo

	width, height int
}

// NewPings creates the pings view.
func NewPings(session *workspace.Session, store *data.Store) *Pings {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No ping threads found.")
	list.SetFocused(true)

	return &Pings{
		session:  session,
		store:    store,
		styles:   styles,
		list:     list,
		spinner:  s,
		loading:  true,
		roomMeta: make(map[string]workspace.PingRoomInfo),
	}
}

func (v *Pings) Title() string { return "Pings" }

func (v *Pings) ShortHelp() []key.Binding {
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
	return tea.Batch(v.spinner.Tick, v.fetchPings())
}

func (v *Pings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.PingRoomsLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading pings")
		}
		v.syncRooms(msg.Rooms)
		return v, nil

	case workspace.RefreshMsg:
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.fetchPings())

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
				Extra:       r.LastAt,
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

func (v *Pings) fetchPings() tea.Cmd {
	ms := v.session.MultiStore()
	ctx := v.session.Context()
	return func() tea.Msg {
		accounts := ms.Accounts()
		if len(accounts) == 0 {
			return workspace.PingRoomsLoadedMsg{}
		}

		var allRooms []workspace.PingRoomInfo
		results := ms.FanOut(ctx, func(acct data.AccountInfo, client *basecamp.AccountClient) (any, error) {
			campfires, err := client.Campfires().List(ctx)
			if err != nil {
				return nil, err
			}

			var rooms []workspace.PingRoomInfo
			for _, cf := range campfires.Campfires {
				// Heuristic: 1:1 campfires are not associated with a project bucket
				// or have a specific title pattern. The Basecamp API doesn't
				// clearly distinguish ping rooms, so we look for campfires
				// where the title contains a person name or the bucket type
				// suggests a personal context.
				// For now, include all non-project campfires as potential ping rooms.
				if cf.Bucket != nil && cf.Bucket.Type == "Project" {
					continue // skip project campfires
				}

				// Try to get the latest line
				var lastMsg, lastAt string
				var lastAtTS int64
				lines, err := client.Campfires().ListLines(ctx, 0, cf.ID)
				if err == nil && len(lines.Lines) > 0 {
					last := lines.Lines[len(lines.Lines)-1]
					if last.Creator != nil {
						lastMsg = last.Creator.Name + ": "
					}
					lastMsg += truncate(last.Content, 40)
					lastAt = last.CreatedAt.Format("Jan 2 3:04pm")
					lastAtTS = last.CreatedAt.Unix()
				}

				var projectID int64
				if cf.Bucket != nil {
					projectID = cf.Bucket.ID
				}

				rooms = append(rooms, workspace.PingRoomInfo{
					CampfireID:  cf.ID,
					ProjectID:   projectID,
					PersonName:  cf.Title,
					Account:     acct.Name,
					AccountID:   acct.ID,
					LastMessage: lastMsg,
					LastAt:      lastAt,
					LastAtTS:    lastAtTS,
				})
			}
			return rooms, nil
		})

		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if rooms, ok := r.Data.([]workspace.PingRoomInfo); ok {
				allRooms = append(allRooms, rooms...)
			}
		}

		// Sort by last message time descending (numeric timestamp)
		sort.Slice(allRooms, func(i, j int) bool {
			return allRooms[i].LastAtTS > allRooms[j].LastAtTS
		})

		return workspace.PingRoomsLoadedMsg{Rooms: allRooms}
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
