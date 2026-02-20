package views

import (
	"fmt"
	"strconv"

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

// Messages is the split-pane view for a project's message board.
type Messages struct {
	session *workspace.Session
	pool    *data.Pool[[]data.MessageInfo]
	styles  *tui.Styles

	// Layout
	split         *widget.SplitPane
	list          *widget.List
	preview       *widget.Preview
	width, height int

	// Loading
	spinner  spinner.Model
	loading  bool
	fetching int64 // message ID currently being fetched for preview (0 = none)

	// Data
	messages      []workspace.MessageInfo
	cachedDetail  map[int64]*workspace.MessageDetailLoadedMsg
	selectedMsgID int64
}

// NewMessages creates the split-pane messages view.
func NewMessages(session *workspace.Session) *Messages {
	styles := session.Styles()
	scope := session.Scope()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	list := widget.NewList(styles)
	list.SetEmptyText("No messages found.")
	list.SetFocused(true)

	preview := widget.NewPreview(styles)
	split := widget.NewSplitPane(styles, 0.35)

	pool := session.Hub().Messages(scope.ProjectID, scope.ToolID)

	return &Messages{
		session:      session,
		pool:         pool,
		styles:       styles,
		split:        split,
		list:         list,
		preview:      preview,
		spinner:      s,
		loading:      true,
		cachedDetail: make(map[int64]*workspace.MessageDetailLoadedMsg),
	}
}

// Title implements View.
func (v *Messages) Title() string {
	return "Message Board"
}

// ShortHelp implements View.
func (v *Messages) ShortHelp() []key.Binding {
	if v.list.Filtering() {
		return filterHints()
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new message")),
		key.NewBinding(key.WithKeys("b", "B"), key.WithHelp("b", "boost")),
	}
}

// FullHelp implements View.
func (v *Messages) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Messages) StartFilter() { v.list.StartFilter() }

// InputActive implements workspace.InputCapturer.
func (v *Messages) InputActive() bool { return v.list.Filtering() }

// SetSize implements View.
func (v *Messages) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.split.SetSize(w, h)
	v.list.SetSize(v.split.LeftWidth(), h)
	v.preview.SetSize(v.split.RightWidth(), h)
}

// Init implements tea.Model.
func (v *Messages) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.messages = snap.Data
		v.syncList()
		v.loading = false
		if snap.Fresh() {
			// Auto-select first message
			if item := v.list.Selected(); item != nil {
				return v.selectMessage(item.ID)
			}
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *Messages) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.messages = snap.Data
				v.syncList()
				v.loading = false
				// Auto-select first message if nothing selected yet
				if v.selectedMsgID == 0 {
					if item := v.list.Selected(); item != nil {
						return v, v.selectMessage(item.ID)
					}
				}
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading messages")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		}
		return v, nil

	case workspace.MessageDetailLoadedMsg:
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading message detail")
		}
		v.cachedDetail[msg.MessageID] = &msg
		// Only update preview if this is still the selected message
		if msg.MessageID == v.selectedMsgID {
			v.fetching = 0
			v.showPreview(&msg)
		}
		return v, nil

	case workspace.MessageCreatedMsg:
		// A message was created from the compose view — refresh the list
		if msg.Err == nil {
			return v, v.pool.Fetch(v.session.Hub().ProjectContext())
		}

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		v.cachedDetail = make(map[int64]*workspace.MessageDetailLoadedMsg)
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

	case workspace.BoostCreatedMsg:
		// Optimistically update the boost count in the message list
		if msg.Target.RecordingID != 0 {
			items := v.list.Items()
			for i, item := range items {
				var itemID int64
				fmt.Sscanf(item.ID, "%d", &itemID)
				if itemID == msg.Target.RecordingID {
					item.Boosts++
					items[i] = item
					break
				}
			}
			v.list.SetItems(items)
		}
		return v, nil

	case spinner.TickMsg:
		if v.loading || v.fetching != 0 {
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

func (v *Messages) handleKey(msg tea.KeyMsg) tea.Cmd {
	keys := workspace.DefaultListKeyMap()

	switch {
	case msg.String() == "b" || msg.String() == "B":
		return v.boostSelectedMessage()

	case msg.String() == "n":
		return v.composeNewMessage()
	case key.Matches(msg, keys.Open):
		return v.openSelectedMessage()
	default:
		prevIdx := v.list.SelectedIndex()
		cmd := v.list.Update(msg)

		if v.list.SelectedIndex() != prevIdx {
			if item := v.list.Selected(); item != nil {
				return tea.Batch(cmd, v.selectMessage(item.ID))
			}
		}
		return cmd
	}
}

func (v *Messages) openSelectedMessage() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	var msgID int64
	fmt.Sscanf(item.ID, "%d", &msgID)

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Message",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = msgID
	scope.RecordingType = "Message"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Messages) composeNewMessage() tea.Cmd {
	scope := v.session.Scope()
	return workspace.Navigate(workspace.ViewCompose, scope)
}

func (v *Messages) selectMessage(id string) tea.Cmd {
	var msgID int64
	fmt.Sscanf(id, "%d", &msgID)
	if msgID == v.selectedMsgID {
		return nil
	}
	v.selectedMsgID = msgID

	// If we have a cached detail, show it immediately
	if cached, ok := v.cachedDetail[msgID]; ok {
		v.fetching = 0
		v.showPreview(cached)
		return nil
	}

	v.fetching = msgID
	v.clearPreview()
	return tea.Batch(v.spinner.Tick, v.fetchMessageDetail(msgID))
}

func (v *Messages) showPreview(detail *workspace.MessageDetailLoadedMsg) {
	v.preview.SetTitle(detail.Subject)

	fields := []widget.PreviewField{
		{Key: "By", Value: detail.Creator},
		{Key: "Date", Value: detail.CreatedAt},
	}
	if detail.Category != "" {
		fields = append(fields, widget.PreviewField{Key: "Category", Value: detail.Category})
	}
	v.preview.SetFields(fields)
	v.preview.SetBody(detail.Content)

	// Re-apply size so the preview recalculates content height
	v.preview.SetSize(v.split.RightWidth(), v.height)
}

func (v *Messages) clearPreview() {
	v.preview.SetTitle("")
	v.preview.SetFields(nil)
	v.preview.SetBody("")
}

// View implements tea.Model.
func (v *Messages) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading messages...")
	}

	left := v.list.View()

	var right string
	if v.fetching != 0 {
		right = lipgloss.NewStyle().
			Padding(0, 1).
			Render(v.spinner.View() + " Loading message...")
	} else {
		right = v.preview.View()
	}

	v.split.SetContent(left, right)
	return v.split.View()
}

// -- Data sync

func (v *Messages) syncList() {
	items := make([]widget.ListItem, 0, len(v.messages))
	for _, m := range v.messages {
		desc := m.Creator
		if m.CreatedAt != "" {
			if desc != "" {
				desc += " - "
			}
			desc += m.CreatedAt
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", m.ID),
			Title:       m.Subject,
			Description: desc,
			Boosts:      m.GetBoosts().Count,
		})
	}
	v.list.SetItems(items)
}

// -- Commands (tea.Cmd factories)

func (v *Messages) fetchMessageDetail(messageID int64) tea.Cmd {
	projectID := v.session.Scope().ProjectID
	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		msg, err := client.Messages().Get(ctx, projectID, messageID)
		if err != nil {
			return workspace.MessageDetailLoadedMsg{MessageID: messageID, Err: err}
		}

		creator := ""
		if msg.Creator != nil {
			creator = msg.Creator.Name
		}
		category := ""
		if msg.Category != nil {
			category = msg.Category.Name
		}

		return workspace.MessageDetailLoadedMsg{
			MessageID: messageID,
			Subject:   msg.Subject,
			Creator:   creator,
			CreatedAt: msg.CreatedAt.Format("Jan 2, 2006"),
			Category:  category,
			Content:   msg.Content,
		}
	}
}

func (v *Messages) boostSelectedMessage() tea.Cmd {
	item := v.list.Selected()
	if item == nil {
		return nil
	}
	id, err := strconv.ParseInt(item.ID, 10, 64)
	if err != nil {
		return nil
	}
	return func() tea.Msg {
		return workspace.OpenBoostPickerMsg{
			Target: workspace.BoostTarget{
				ProjectID:   v.session.Scope().ProjectID,
				RecordingID: id,
				Title:       item.Title,
			},
		}
	}
}
