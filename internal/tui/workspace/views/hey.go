package views

import (
	"fmt"
	"strings"
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

// Hey-local mutation result messages.
type heyCompleteResultMsg struct {
	itemID string
	err    error
}
type heyTrashResultMsg struct {
	itemID string
	err    error
}
type heyTrashTimeoutMsg struct{}

// Hey is the activity feed view showing recently updated recordings across
// all accounts. It replaces the empty notifications stub with real data
// from Recordings().List() fan-out.
type Hey struct {
	session *workspace.Session
	pool    *data.Pool[[]data.ActivityEntryInfo]
	styles  *tui.Styles

	list    *widget.List
	spinner spinner.Model
	loading bool

	// Entries metadata for navigation
	entryMeta map[string]workspace.ActivityEntryInfo
	excluded  map[string]bool // items completed/trashed, pending pool refresh

	// Double-press trash confirmation
	trashPending   bool
	trashPendingID string

	width, height int
}

// NewHey creates the Hey! activity feed view.
func NewHey(session *workspace.Session) *Hey {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyMessage(empty.NoRecordings("activity"))
	list.SetFocused(true)

	pool := session.Hub().HeyActivity()

	return &Hey{
		session:   session,
		pool:      pool,
		styles:    styles,
		list:      list,
		spinner:   s,
		loading:   true,
		entryMeta: make(map[string]workspace.ActivityEntryInfo),
		excluded:  make(map[string]bool),
	}
}

func (v *Hey) Title() string { return "Hey!" }

// FocusedItem implements workspace.FocusedRecording.
func (v *Hey) FocusedItem() workspace.FocusedItemScope {
	item := v.list.Selected()
	if item == nil {
		return workspace.FocusedItemScope{}
	}
	meta, ok := v.entryMeta[item.ID]
	if !ok {
		return workspace.FocusedItemScope{}
	}
	return workspace.FocusedItemScope{
		AccountID:   meta.AccountID,
		ProjectID:   meta.ProjectID,
		RecordingID: meta.ID,
	}
}

func (v *Hey) ShortHelp() []key.Binding {
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

func (v *Hey) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Hey) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Hey) InputActive() bool { return v.list.Filtering() }

func (v *Hey) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.list.SetSize(w, h)
}

func (v *Hey) Init() tea.Cmd {
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

func (v *Hey) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.State == data.StateFresh {
				v.excluded = make(map[string]bool) // API response reflects mutations
			}
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
				return v, workspace.ReportError(snap.Err, "loading activity")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case heyCompleteResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "completing todo")
		}
		v.excluded[msg.itemID] = true
		snap := v.pool.Get()
		if snap.Usable() {
			v.syncEntries(snap.Data)
		}
		return v, workspace.SetStatus("Completed", false)

	case heyTrashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing recording")
		}
		v.excluded[msg.itemID] = true
		snap := v.pool.Get()
		if snap.Usable() {
			v.syncEntries(snap.Data)
		}
		return v, workspace.SetStatus("Trashed", false)

	case heyTrashTimeoutMsg:
		v.trashPending = false
		v.trashPendingID = ""
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

func (v *Hey) View() string {
	if v.loading && v.list.Len() == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading activity…")
	}
	return v.list.View()
}

func (v *Hey) syncEntries(entries []workspace.ActivityEntryInfo) {
	v.entryMeta = make(map[string]workspace.ActivityEntryInfo, len(entries))
	items := make([]widget.ListItem, 0, len(entries)+4) // room for time headers
	accounts := sessionAccounts(v.session)

	// Group by time bucket
	now := time.Now()
	var justNow, hourAgo, today, yesterday, older []workspace.ActivityEntryInfo

	for _, e := range entries {
		age := now.Unix() - e.UpdatedAtTS
		switch {
		case age < 600: // 10 min
			justNow = append(justNow, e)
		case age < 3600: // 1 hour
			hourAgo = append(hourAgo, e)
		case age < 86400 && now.Day() == time.Unix(e.UpdatedAtTS, 0).Day():
			today = append(today, e)
		case age < 172800:
			yesterday = append(yesterday, e)
		default:
			older = append(older, e)
		}
	}

	addGroup := func(label string, group []workspace.ActivityEntryInfo) {
		if len(group) == 0 {
			return
		}
		items = append(items, widget.ListItem{Title: label, Header: true})
		for _, e := range group {
			id := fmt.Sprintf("%s:%d", e.AccountID, e.ID)
			if v.excluded[id] {
				continue
			}
			v.entryMeta[id] = e
			desc := e.Account
			if e.Project != "" {
				desc += " > " + e.Project
			}
			items = append(items, widget.ListItem{
				ID:          id,
				Title:       e.Title,
				Description: desc,
				Extra:       accountExtra(accounts, e.AccountID, e.Type),
			})
		}
	}

	addGroup("Just Now", justNow)
	addGroup("1 Hour Ago", hourAgo)
	addGroup("Today", today)
	addGroup("Yesterday", yesterday)
	addGroup("Older", older)

	v.list.SetItems(items)
}

func (v *Hey) openSelected() tea.Cmd {
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
	scope.OriginView = "Hey!"
	scope.OriginHint = meta.Type
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Hey) completeSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	meta, ok := v.entryMeta[item.ID]
	if !ok {
		return nil
	}
	if !strings.EqualFold(meta.Type, "Todo") {
		return workspace.SetStatus("Can only complete todos", false)
	}

	hub := v.session.Hub()
	ctx := hub.Global().Context()
	itemID := item.ID
	return func() tea.Msg {
		err := hub.CompleteTodo(ctx, meta.AccountID, meta.ProjectID, meta.ID)
		return heyCompleteResultMsg{itemID: itemID, err: err}
	}
}

func (v *Hey) trashSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	meta, ok := v.entryMeta[item.ID]
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
			return heyTrashResultMsg{itemID: itemID, err: err}
		}
	}
	v.trashPending = true
	v.trashPendingID = item.ID
	return tea.Batch(
		workspace.SetStatus("Press t again to trash", false),
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return heyTrashTimeoutMsg{} }),
	)
}

func (v *Hey) boostSelected() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	meta, ok := v.entryMeta[item.ID]
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return workspace.OpenBoostPickerMsg{
			Target: workspace.BoostTarget{
				ProjectID:   meta.ProjectID,
				RecordingID: meta.ID,
				AccountID:   meta.AccountID,
				Title:       meta.Title,
			},
		}
	}
}

func (v *Hey) schedulePoll() tea.Cmd {
	interval := v.pool.PollInterval()
	if interval == 0 {
		return nil
	}
	key := v.pool.Key()
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: key}
	})
}
