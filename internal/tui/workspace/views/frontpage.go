package views

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// burstTier classifies room activity level.
type burstTier int

const (
	burstHot  burstTier = iota // last msg < 2 min
	burstWarm                  // last msg < 30 min
	burstCold                  // everything else
)

// FrontPage is the campfire overview view showing all campfire rooms
// with burst tiers (Hot/Warm/Cold) based on recent activity.
type FrontPage struct {
	session *workspace.Session
	styles  *tui.Styles

	roomPool   *data.Pool[[]data.BonfireRoomConfig]
	digestPool *data.Pool[[]data.BonfireDigestEntry]

	list    *widget.List
	spinner spinner.Model

	// Digest entries indexed by list item ID for navigation metadata.
	digestByID map[string]data.BonfireDigestEntry
	roomByID   map[string]data.BonfireRoomConfig

	pollGen uint64

	width, height int
}

// NewFrontPage creates a new FrontPage view.
func NewFrontPage(session *workspace.Session) *FrontPage {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No campfire rooms found.")
	list.SetFocused(true)

	return &FrontPage{
		session:    session,
		styles:     styles,
		list:       list,
		spinner:    s,
		digestByID: make(map[string]data.BonfireDigestEntry),
		roomByID:   make(map[string]data.BonfireRoomConfig),
	}
}

func (f *FrontPage) Init() tea.Cmd {
	hub := f.session.Hub()
	f.roomPool = hub.BonfireRooms()
	f.digestPool = hub.BonfireDigest()
	globalCtx := hub.Global().Context()

	cmds := []tea.Cmd{f.spinner.Tick}

	// Rooms
	snap := f.roomPool.Get()
	if snap.Usable() {
		f.syncRooms(snap.Data)
	}
	if !snap.Fresh() {
		cmds = append(cmds, f.roomPool.FetchIfStale(globalCtx))
	}

	// Digest
	snap2 := f.digestPool.Get()
	if snap2.Usable() {
		f.syncDigest(snap2.Data)
	}
	if !snap2.Fresh() {
		cmds = append(cmds, f.digestPool.FetchIfStale(globalCtx))
	}

	f.rebuildList()
	return tea.Batch(cmds...)
}

func (f *FrontPage) Update(msg tea.Msg) (workspace.View, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		switch msg.Key {
		case f.roomPool.Key():
			snap := f.roomPool.Get()
			if snap.Usable() {
				f.syncRooms(snap.Data)
			}
		case f.digestPool.Key():
			snap := f.digestPool.Get()
			if snap.Usable() {
				f.syncDigest(snap.Data)
			}
		}
		f.rebuildList()
		return f, nil

	case data.PollMsg:
		if msg.Tag == "frontpage" && msg.Gen == f.pollGen {
			globalCtx := f.session.Hub().Global().Context()
			return f, tea.Batch(
				f.digestPool.FetchIfStale(globalCtx),
				f.schedulePoll(),
			)
		}

	case workspace.RefreshMsg:
		f.roomPool.Invalidate()
		f.digestPool.Invalidate()
		globalCtx := f.session.Hub().Global().Context()
		return f, tea.Batch(
			f.spinner.Tick,
			f.roomPool.Fetch(globalCtx),
			f.digestPool.Fetch(globalCtx),
		)

	case workspace.FocusMsg:
		globalCtx := f.session.Hub().Global().Context()
		return f, tea.Batch(
			f.roomPool.FetchIfStale(globalCtx),
			f.digestPool.FetchIfStale(globalCtx),
			f.schedulePoll(),
		)

	case workspace.TerminalFocusMsg:
		return f, f.schedulePoll()

	case spinner.TickMsg:
		if f.anyLoading() && f.list.Len() == 0 {
			var cmd tea.Cmd
			f.spinner, cmd = f.spinner.Update(msg)
			return f, cmd
		}

	case tea.KeyPressMsg:
		if f.list.Filtering() {
			return f, f.list.Update(msg)
		}
		keys := workspace.DefaultListKeyMap()
		switch {
		case key.Matches(msg, keys.Open):
			return f, f.openSelected()
		default:
			return f, f.list.Update(msg)
		}
	}

	return f, nil
}

func (f *FrontPage) View() string {
	if f.anyLoading() && f.list.Len() == 0 {
		return lipgloss.NewStyle().
			Width(f.width).
			Height(f.height).
			Padding(1, 2).
			Render(f.spinner.View() + " Loading campfire overview\u2026")
	}
	return f.list.View()
}

func (f *FrontPage) syncRooms(rooms []data.BonfireRoomConfig) {
	f.roomByID = make(map[string]data.BonfireRoomConfig, len(rooms))
	for _, r := range rooms {
		f.roomByID[r.Key()] = r
	}
}

func (f *FrontPage) syncDigest(entries []data.BonfireDigestEntry) {
	f.digestByID = make(map[string]data.BonfireDigestEntry, len(entries))
	for _, e := range entries {
		f.digestByID[e.Key()] = e
	}
}

func (f *FrontPage) rebuildList() {
	theme := f.styles.Theme()

	// Prefer digest entries (sorted by recency from hub). Fall back to rooms.
	digestSnap := f.digestPool.Get()
	roomSnap := f.roomPool.Get()

	var items []widget.ListItem

	if digestSnap.Usable() && len(digestSnap.Data) > 0 {
		items = append(items, widget.ListItem{Title: "Campfires", Header: true})
		items = append(items, f.digestItems(digestSnap.Data, theme)...)
	} else if roomSnap.Usable() && len(roomSnap.Data) > 0 {
		// Fallback: just room names
		items = append(items, widget.ListItem{Title: "Campfires", Header: true})
		for _, r := range roomSnap.Data {
			id := "room:" + r.Key()
			colorIdx := r.Color(len(theme.RoomColors))
			title := r.ProjectName
			if colorIdx < len(theme.RoomColors) {
				title = lipgloss.NewStyle().Foreground(theme.RoomColors[colorIdx]).Render(r.ProjectName)
			}
			items = append(items, widget.ListItem{
				ID:    id,
				Title: title,
			})
		}
	}

	f.list.SetItems(items)
}

func (f *FrontPage) digestItems(entries []data.BonfireDigestEntry, theme tui.Theme) []widget.ListItem {
	items := make([]widget.ListItem, 0, len(entries))
	for _, e := range entries {
		id := "digest:" + e.Key()
		f.digestByID[e.Key()] = e

		// Room-colored title with activity indicator
		colorIdx := e.Color(len(theme.RoomColors))
		title := e.RoomName
		if colorIdx < len(theme.RoomColors) {
			title = lipgloss.NewStyle().Foreground(theme.RoomColors[colorIdx]).Render(e.RoomName)
		}

		// Activity indicator prefix
		tier := classifyBurst(e)
		switch tier {
		case burstHot:
			title = lipgloss.NewStyle().Foreground(theme.Error).Render("\u25cf ") + title
		case burstWarm:
			title = lipgloss.NewStyle().Foreground(theme.Warning).Render("\u25cb ") + title
		default:
			title = lipgloss.NewStyle().Foreground(theme.Muted).Render("\u25cb ") + title
		}

		// Description: author + message preview
		var desc string
		if e.LastAuthor != "" && e.LastMessage != "" {
			desc = e.LastAuthor + ": " + e.LastMessage
		} else if e.LastMessage != "" {
			desc = e.LastMessage
		}

		// Relative time in the Extra column
		extra := frontpageRelativeTime(e.LastAtTS)

		items = append(items, widget.ListItem{
			ID:          id,
			Title:       title,
			Description: desc,
			Extra:       extra,
		})
	}
	return items
}

func (f *FrontPage) openSelected() tea.Cmd {
	item := f.list.Selected()
	if item == nil || item.Header {
		return nil
	}

	// Navigate to the single-room campfire view for the selected room
	scope := f.session.Scope()

	// Extract room info from digest or room maps
	var roomKey string
	if strings.HasPrefix(item.ID, "digest:") {
		roomKey = strings.TrimPrefix(item.ID, "digest:")
	} else if strings.HasPrefix(item.ID, "room:") {
		roomKey = strings.TrimPrefix(item.ID, "room:")
	}

	if entry, ok := f.digestByID[roomKey]; ok {
		scope.AccountID = entry.AccountID
		scope.ProjectID = entry.ProjectID
		scope.ToolType = "chat"
		scope.ToolID = entry.CampfireID
		return workspace.Navigate(workspace.ViewCampfire, scope)
	}
	if room, ok := f.roomByID[roomKey]; ok {
		scope.AccountID = room.AccountID
		scope.ProjectID = room.ProjectID
		scope.ToolType = "chat"
		scope.ToolID = room.CampfireID
		return workspace.Navigate(workspace.ViewCampfire, scope)
	}

	return nil
}

func (f *FrontPage) schedulePoll() tea.Cmd {
	f.pollGen++
	gen := f.pollGen
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: "frontpage", Gen: gen}
	})
}

func (f *FrontPage) anyLoading() bool {
	if f.roomPool == nil || f.digestPool == nil {
		return false
	}
	return poolPending(f.roomPool.Get()) || poolPending(f.digestPool.Get())
}

func frontpageRelativeTime(ts int64) string {
	if ts == 0 {
		return ""
	}
	d := time.Since(time.Unix(ts, 0))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func classifyBurst(entry data.BonfireDigestEntry) burstTier {
	if entry.LastAtTS == 0 {
		return burstCold
	}
	elapsed := time.Since(time.Unix(entry.LastAtTS, 0))
	switch {
	case elapsed < 2*time.Minute:
		return burstHot
	case elapsed < 30*time.Minute:
		return burstWarm
	default:
		return burstCold
	}
}

func (f *FrontPage) Title() string { return "Front Page" }

func (f *FrontPage) ShortHelp() []key.Binding {
	if f.list.Filtering() {
		return filterHints()
	}
	if f.anyLoading() && f.list.Len() == 0 {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open campfire")),
	}
}

func (f *FrontPage) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open campfire")),
		},
	}
}

// StartFilter implements Filterable.
func (f *FrontPage) StartFilter() { f.list.StartFilter() }

// InputActive implements InputCapturer.
func (f *FrontPage) InputActive() bool { return f.list.Filtering() }

func (f *FrontPage) SetSize(w, h int) {
	f.width = w
	f.height = h
	f.list.SetSize(w, h)
}
