package views

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// Messages is the split-pane view for a project's message board.
type Messages struct {
	session *workspace.Session
	store   *data.Store
	styles  *tui.Styles

	// IDs
	projectID int64
	boardID   int64

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
func NewMessages(session *workspace.Session, store *data.Store) *Messages {
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

	return &Messages{
		session:      session,
		store:        store,
		styles:       styles,
		projectID:    scope.ProjectID,
		boardID:      scope.ToolID,
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
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new message")),
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
	return tea.Batch(v.spinner.Tick, v.fetchMessages())
}

// Update implements tea.Model.
func (v *Messages) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.MessagesLoadedMsg:
		v.loading = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "loading messages")
		}
		v.messages = msg.Messages
		v.syncList()
		// Auto-select first message
		if item := v.list.Selected(); item != nil {
			return v, v.selectMessage(item.ID)
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
			return v, v.fetchMessages()
		}

	case workspace.RefreshMsg:
		v.loading = true
		v.cachedDetail = make(map[int64]*workspace.MessageDetailLoadedMsg)
		return v, tea.Batch(v.spinner.Tick, v.fetchMessages())

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
		})
	}
	v.list.SetItems(items)
}

// -- Commands (tea.Cmd factories)

func (v *Messages) fetchMessages() tea.Cmd {
	projectID := v.projectID
	boardID := v.boardID
	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		result, err := client.Messages().List(ctx, projectID, boardID, &basecamp.MessageListOptions{})
		if err != nil {
			return workspace.MessagesLoadedMsg{Err: err}
		}

		infos := make([]workspace.MessageInfo, 0, len(result.Messages))
		for _, m := range result.Messages {
			creator := ""
			if m.Creator != nil {
				creator = m.Creator.Name
			}
			category := ""
			if m.Category != nil {
				category = m.Category.Name
			}
			infos = append(infos, workspace.MessageInfo{
				ID:        m.ID,
				Subject:   m.Subject,
				Creator:   creator,
				CreatedAt: m.CreatedAt.Format("Jan 2, 2006"),
				Category:  category,
			})
		}
		return workspace.MessagesLoadedMsg{Messages: infos}
	}
}

func (v *Messages) fetchMessageDetail(messageID int64) tea.Cmd {
	projectID := v.projectID
	ctx := v.session.Context()
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
