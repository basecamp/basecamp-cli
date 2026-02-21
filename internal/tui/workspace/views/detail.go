package views

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// detailComment holds a single comment's display data.
type detailComment struct {
	creator   string
	createdAt time.Time
	content   string // HTML body
}

// detailData holds the fetched recording data.
type detailData struct {
	title      string
	recordType string
	content    string // HTML body
	creator    string
	createdAt  time.Time
	assignees  []string
	completed  bool
	dueOn      string
	comments   []detailComment
	boosts     int
	subscribed bool
}

// detailLoadedMsg is sent when the recording detail is fetched.
type detailLoadedMsg struct {
	data detailData
	err  error
}

// Detail-local mutation result messages.
type todoToggleResultMsg struct {
	completed bool
	err       error
}

type editTitleResultMsg struct {
	title string
	err   error
}
type subscribeResultMsg struct {
	subscribed bool
	err        error
}

type trashTimeoutMsg struct{}
type trashResultMsg struct{ err error }

// Detail shows a single recording with its content and metadata.
type Detail struct {
	session *workspace.Session
	styles  *tui.Styles

	recordingID   int64
	recordingType string
	originView    string
	originHint    string
	data          *detailData
	preview       *widget.Preview
	spinner       spinner.Model
	loading       bool

	// Inline comment composer
	composer   *widget.Composer
	composing  bool
	submitting bool

	// Inline title editing
	editing   bool
	editInput textinput.Model

	// Double-press trash confirmation
	trashPending bool

	width, height int
}

// NewDetail creates a detail view for a specific recording.
func NewDetail(session *workspace.Session, recordingID int64, recordingType, originView, originHint string) *Detail {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	// Create upload function for comment attachments — capture client at construction time.
	client := session.AccountClient()
	uploadFn := func(ctx context.Context, path, filename, contentType string) (string, error) {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		resp, err := client.Attachments().Create(ctx, filename, contentType, io.Reader(f))
		if err != nil {
			return "", err
		}
		return resp.AttachableSGID, nil
	}

	comp := widget.NewComposer(styles,
		widget.WithMode(widget.ComposerRich),
		widget.WithAutoExpand(false),
		widget.WithUploadFn(uploadFn),
		widget.WithContext(session.Context()),
		widget.WithPlaceholder("Write a comment..."),
	)

	return &Detail{
		session:       session,
		styles:        styles,
		recordingID:   recordingID,
		recordingType: recordingType,
		originView:    originView,
		originHint:    originHint,
		preview:       widget.NewPreview(styles),
		spinner:       s,
		loading:       true,
		composer:      comp,
	}
}

func (v *Detail) Title() string {
	if v.data != nil {
		return v.data.title
	}
	return "Detail"
}

// InputActive implements workspace.InputCapturer.
func (v *Detail) InputActive() bool {
	return v.composing || v.editing
}

// IsModal implements workspace.ModalActive.
func (v *Detail) IsModal() bool {
	return v.composing || v.editing
}

func (v *Detail) ShortHelp() []key.Binding {
	if v.editing {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	hints := []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "scroll")),
	}
	if v.data != nil && strings.EqualFold(v.data.recordType, "Todo") {
		verb := "complete"
		if v.data.completed {
			verb = "reopen"
		}
		hints = append(hints, key.NewBinding(key.WithKeys("x"), key.WithHelp("x", verb)))
	}
	hints = append(hints,
		key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit title")),
		key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "subscribe")),
		key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comment")),
		key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "boost")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trash")),
	)
	if v.session != nil && v.session.Scope().ProjectID != 0 {
		hints = append(hints, key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "project")))
	}
	if v.composing {
		hints = append(hints,
			key.NewBinding(key.WithKeys("ctrl+enter"), key.WithHelp("ctrl+enter", "post comment")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		)
	}
	return hints
}

func (v *Detail) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

func (v *Detail) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.relayout()
}

func (v *Detail) relayout() {
	if v.composing {
		composerHeight := 6
		previewHeight := v.height - composerHeight - 1 // -1 for separator
		if previewHeight < 3 {
			previewHeight = 3
		}
		v.preview.SetSize(v.width-2, previewHeight)
		v.composer.SetSize(v.width-2, composerHeight)
	} else {
		v.preview.SetSize(v.width-2, v.height)
	}
}

func (v *Detail) Init() tea.Cmd {
	return tea.Batch(v.spinner.Tick, v.fetchDetail())
}

func (v *Detail) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case detailLoadedMsg:
		v.loading = false
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "loading detail")
		}
		v.data = &msg.data
		v.syncPreview()
		return v, nil

	case workspace.CommentCreatedMsg:
		v.submitting = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "posting comment")
		}
		v.composing = false
		v.composer.Reset()
		v.relayout()
		// Refresh to show the new comment
		v.loading = true
		return v, tea.Batch(
			v.spinner.Tick,
			v.fetchDetail(),
			workspace.SetStatus("Comment added", false),
		)

	case widget.ComposerSubmitMsg:
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "composing comment")
		}
		v.submitting = true
		return v, v.postComment(msg.Content)

	case widget.EditorReturnMsg:
		return v, v.composer.HandleEditorReturn(msg)

	case widget.AttachFileRequestMsg:
		if v.composing {
			return v, workspace.SetStatus("Paste a file path or drag a file into the terminal", false)
		}

	case spinner.TickMsg:
		if v.loading {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case workspace.BoostCreatedMsg:
		// Refresh to get the updated boost count
		if msg.Target.RecordingID == v.recordingID {
			v.loading = true
			return v, tea.Batch(
				v.spinner.Tick,
				v.fetchDetail(),
			)
		}
		return v, nil

	case todoToggleResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "toggling todo")
		}
		v.data.completed = msg.completed
		v.syncPreview()
		verb := "Completed"
		if !msg.completed {
			verb = "Reopened"
		}
		return v, workspace.SetStatus(verb, false)

	case editTitleResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "editing title")
		}
		v.editing = false
		v.data.title = msg.title
		v.syncPreview()
		return v, workspace.SetStatus("Title updated", false)

	case subscribeResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "updating subscription")
		}
		v.data.subscribed = msg.subscribed
		verb := "Subscribed"
		if !msg.subscribed {
			verb = "Unsubscribed"
		}
		return v, workspace.SetStatus(verb, false)

	case trashResultMsg:
		if msg.err != nil {
			return v, workspace.ReportError(msg.err, "trashing recording")
		}
		return v, tea.Batch(workspace.SetStatus("Trashed", false), workspace.NavigateBack())

	case trashTimeoutMsg:
		v.trashPending = false
		return v, nil

	case tea.KeyMsg:
		if v.loading {
			return v, nil
		}
		return v, v.handleKey(msg)
	}

	// Forward other messages to composer
	if v.composing {
		if cmd := v.composer.Update(msg); cmd != nil {
			return v, cmd
		}
	}

	return v, nil
}

func (v *Detail) handleKey(msg tea.KeyMsg) tea.Cmd {
	if v.editing {
		return v.handleEditingKey(msg)
	}
	if v.composing {
		return v.handleComposingKey(msg)
	}

	// Any non-t key resets trash confirmation
	if msg.String() != "t" {
		v.trashPending = false
	}

	switch msg.String() {
	case "e":
		return v.startEditTitle()
	case "s":
		return v.toggleSubscribe()
	case "b", "B":
		if v.data == nil {
			return nil
		}
		return func() tea.Msg {
			return workspace.OpenBoostPickerMsg{
				Target: workspace.BoostTarget{
					ProjectID:   v.session.Scope().ProjectID,
					RecordingID: v.session.Scope().RecordingID,
					Title:       v.data.title,
				},
			}
		}

	case "c":
		v.composing = true
		v.relayout()
		return v.composer.Focus()
	case "x":
		return v.toggleComplete()
	case "t":
		if v.data == nil {
			return nil
		}
		if v.trashPending {
			v.trashPending = false
			return v.trashRecording()
		}
		v.trashPending = true
		return tea.Batch(
			workspace.SetStatus("Press t again to trash", false),
			v.trashConfirmTimeout(),
		)
	case "g":
		return v.goToProject()
	case "j", "down":
		v.preview.ScrollDown(1)
	case "k", "up":
		v.preview.ScrollUp(1)
	case "ctrl+d":
		v.preview.ScrollDown(v.height / 2)
	case "ctrl+u":
		v.preview.ScrollUp(v.height / 2)
	}
	return nil
}

func (v *Detail) toggleComplete() tea.Cmd {
	if v.data == nil || !strings.EqualFold(v.data.recordType, "Todo") {
		return workspace.SetStatus("Can only complete todos", false)
	}
	newState := !v.data.completed
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	return func() tea.Msg {
		var err error
		if newState {
			err = hub.CompleteTodo(ctx, scope.AccountID, scope.ProjectID, v.recordingID)
		} else {
			err = hub.UncompleteTodo(ctx, scope.AccountID, scope.ProjectID, v.recordingID)
		}
		return todoToggleResultMsg{completed: newState, err: err}
	}
}

func (v *Detail) trashConfirmTimeout() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return trashTimeoutMsg{}
	})
}

func (v *Detail) trashRecording() tea.Cmd {
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := hub.ProjectContext()
	return func() tea.Msg {
		err := hub.TrashRecording(ctx, scope.AccountID, scope.ProjectID, v.recordingID)
		return trashResultMsg{err: err}
	}
}

func (v *Detail) goToProject() tea.Cmd {
	scope := v.session.Scope()
	if scope.ProjectID == 0 {
		return workspace.SetStatus("No project context", false)
	}
	return workspace.Navigate(workspace.ViewDock, scope)
}

func (v *Detail) startEditTitle() tea.Cmd {
	if v.data == nil {
		return nil
	}
	v.editing = true
	v.editInput = textinput.New()
	v.editInput.SetValue(v.data.title)
	v.editInput.CharLimit = 256
	v.editInput.Focus()
	return textinput.Blink
}

func (v *Detail) handleEditingKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		title := strings.TrimSpace(v.editInput.Value())
		if title == "" || title == v.data.title {
			v.editing = false
			return nil
		}
		return v.submitEditTitle(title)
	case "esc":
		v.editing = false
		return nil
	default:
		var cmd tea.Cmd
		v.editInput, cmd = v.editInput.Update(msg)
		return cmd
	}
}

func (v *Detail) submitEditTitle(title string) tea.Cmd {
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := v.session.Context()
	recordType := v.data.recordType
	recordingID := v.recordingID

	return func() tea.Msg {
		var err error
		switch strings.ToLower(recordType) {
		case "todo":
			err = hub.UpdateTodo(ctx, scope.AccountID, scope.ProjectID, recordingID,
				&basecamp.UpdateTodoRequest{Content: title})
		case "card":
			err = hub.UpdateCard(ctx, scope.AccountID, scope.ProjectID, recordingID,
				&basecamp.UpdateCardRequest{Title: title})
		default:
			err = fmt.Errorf("editing %s titles is not supported", recordType)
		}
		return editTitleResultMsg{title: title, err: err}
	}
}

func (v *Detail) toggleSubscribe() tea.Cmd {
	if v.data == nil {
		return nil
	}
	scope := v.session.Scope()
	hub := v.session.Hub()
	ctx := v.session.Context()
	wasSubscribed := v.data.subscribed
	recordingID := v.recordingID

	return func() tea.Msg {
		var err error
		if wasSubscribed {
			err = hub.Unsubscribe(ctx, scope.AccountID, scope.ProjectID, recordingID)
		} else {
			err = hub.Subscribe(ctx, scope.AccountID, scope.ProjectID, recordingID)
		}
		return subscribeResultMsg{subscribed: !wasSubscribed, err: err}
	}
}

func (v *Detail) handleComposingKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case msg.String() == "esc":
		v.composing = false
		v.composer.Blur()
		v.relayout()
		return nil
	case msg.Paste:
		text, cmd := v.composer.ProcessPaste(string(msg.Runes))
		v.composer.InsertPaste(text)
		return cmd
	default:
		return v.composer.Update(msg)
	}
}

func (v *Detail) postComment(content widget.ComposerContent) tea.Cmd {
	scope := v.session.Scope()
	recordingID := v.recordingID

	html := richtext.MarkdownToHTML(content.Markdown)
	if len(content.Attachments) > 0 {
		refs := make([]richtext.AttachmentRef, 0, len(content.Attachments))
		for _, att := range content.Attachments {
			if att.Status == widget.AttachUploaded {
				refs = append(refs, richtext.AttachmentRef{
					SGID:        att.SGID,
					Filename:    att.Filename,
					ContentType: att.ContentType,
				})
			}
		}
		html = richtext.EmbedAttachments(html, refs)
	}

	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {
		_, err := client.Comments().Create(ctx, scope.ProjectID, recordingID, &basecamp.CreateCommentRequest{
			Content: html,
		})
		return workspace.CommentCreatedMsg{RecordingID: recordingID, Err: err}
	}
}

func (v *Detail) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading...")
	}

	if v.submitting {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render("Posting comment...")
	}

	if v.composing {
		theme := v.styles.Theme()
		sep := lipgloss.NewStyle().
			Width(v.width - 2).
			Foreground(theme.Border).
			Render("─ Comment ─")
		return lipgloss.NewStyle().Padding(0, 1).Render(
			lipgloss.JoinVertical(lipgloss.Left,
				v.preview.View(),
				sep,
				v.composer.View(),
			),
		)
	}

	view := v.preview.View()
	if v.editing {
		theme := v.styles.Theme()
		label := lipgloss.NewStyle().Foreground(theme.Muted).Render("Title: ")
		view += "\n" + lipgloss.NewStyle().Padding(0, 1).Render(label+v.editInput.View())
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(view)
}

func (v *Detail) syncPreview() {
	if v.data == nil {
		return
	}

	v.preview.SetTitle(v.data.title)

	var fields []widget.PreviewField
	if v.originView != "" {
		hint := v.originView
		if v.originHint != "" {
			hint += " · " + v.originHint
		}
		fields = append(fields, widget.PreviewField{Key: "From", Value: hint})
	}
	if v.data.recordType != "" {
		fields = append(fields, widget.PreviewField{Key: "Type", Value: v.data.recordType})
	}
	if v.data.creator != "" {
		fields = append(fields, widget.PreviewField{Key: "By", Value: v.data.creator})
	}
	if !v.data.createdAt.IsZero() {
		fields = append(fields, widget.PreviewField{Key: "Created", Value: v.data.createdAt.Format("Jan 2, 2006")})
	}
	if v.data.dueOn != "" {
		fields = append(fields, widget.PreviewField{Key: "Due", Value: v.data.dueOn})
	}
	if len(v.data.assignees) > 0 {
		fields = append(fields, widget.PreviewField{Key: "Assigned", Value: strings.Join(v.data.assignees, ", ")})
	}
	if v.data.completed {
		fields = append(fields, widget.PreviewField{Key: "Status", Value: "Completed"})
	}
	if len(v.data.comments) > 0 {
		fields = append(fields, widget.PreviewField{
			Key:   "Comments",
			Value: fmt.Sprintf("%d", len(v.data.comments)),
		})
	}
	if v.data.boosts > 0 {
		fields = append(fields, widget.PreviewField{
			Key:   "Boosts",
			Value: fmt.Sprintf("♥ %d", v.data.boosts),
		})
	}
	v.preview.SetFields(fields)

	body := v.data.content
	if len(v.data.comments) > 0 {
		body += v.buildCommentsHTML()
	}
	if body != "" {
		v.preview.SetBody(body)
	}
}

// buildCommentsHTML renders comments as HTML to be appended to the body content.
// The combined HTML flows through the Content widget's HTML→Markdown→glamour pipeline,
// so everything scrolls together as a single document.
func (v *Detail) buildCommentsHTML() string {
	var b strings.Builder
	b.WriteString("<hr><h3>Comments</h3>")
	for _, c := range v.data.comments {
		b.WriteString("<p><strong>")
		b.WriteString(c.creator)
		b.WriteString("</strong> <em>")
		b.WriteString(c.createdAt.Format("Jan 2, 2006 3:04 PM"))
		b.WriteString("</em></p>")
		b.WriteString(c.content)
	}
	return b.String()
}

func (v *Detail) fetchDetail() tea.Cmd {
	scope := v.session.Scope()
	recordingID := v.recordingID
	recordingType := v.recordingType

	ctx := v.session.Context()
	client := v.session.AccountClient()
	return func() tea.Msg {

		var data detailData

		switch recordingType {
		case "todo", "Todo":
			todo, err := client.Todos().Get(ctx, scope.ProjectID, recordingID)
			if err != nil {
				return detailLoadedMsg{err: err}
			}
			var assignees []string
			for _, a := range todo.Assignees {
				assignees = append(assignees, a.Name)
			}
			creator := ""
			if todo.Creator != nil {
				creator = todo.Creator.Name
			}
			data = detailData{
				title:      todo.Content,
				recordType: "Todo",
				content:    todo.Description,
				creator:    creator,
				createdAt:  todo.CreatedAt,
				assignees:  assignees,
				completed:  todo.Completed,
				dueOn:      todo.DueOn,
				boosts:     todo.BoostsCount,
			}

		case "message", "Message":
			msg, err := client.Messages().Get(ctx, scope.ProjectID, recordingID)
			if err != nil {
				return detailLoadedMsg{err: err}
			}
			creator := ""
			if msg.Creator != nil {
				creator = msg.Creator.Name
			}
			category := ""
			if msg.Category != nil {
				category = msg.Category.Name
			}
			data = detailData{
				title:      msg.Subject,
				recordType: "Message",
				content:    msg.Content,
				creator:    creator,
				createdAt:  msg.CreatedAt,
				dueOn:      category, // reuse field for category display
				boosts:     msg.BoostsCount,
			}

		case "card", "Card":
			card, err := client.Cards().Get(ctx, scope.ProjectID, recordingID)
			if err != nil {
				return detailLoadedMsg{err: err}
			}
			var assignees []string
			for _, a := range card.Assignees {
				assignees = append(assignees, a.Name)
			}
			creator := ""
			if card.Creator != nil {
				creator = card.Creator.Name
			}
			data = detailData{
				title:      card.Title,
				recordType: "Card",
				content:    card.Content,
				creator:    creator,
				createdAt:  card.CreatedAt,
				assignees:  assignees,
				completed:  card.Completed,
				dueOn:      card.DueOn,
				boosts:     card.BoostsCount,
			}

		default:
			// Generic: fetch via raw API and extract common fields
			path := fmt.Sprintf("/buckets/%d/recordings/%d.json", scope.ProjectID, recordingID)
			resp, err := client.Get(ctx, path)
			if err != nil {
				return detailLoadedMsg{err: err}
			}

			// Parse common recording fields from JSON
			var generic struct {
				Title     string    `json:"title"`
				Subject   string    `json:"subject"`
				Content   string    `json:"content"`
				Type      string    `json:"type"`
				CreatedAt time.Time `json:"created_at"`
				Creator   *struct {
					Name string `json:"name"`
				} `json:"creator"`
			}
			if err := resp.UnmarshalData(&generic); err != nil {
				data = detailData{
					title:      fmt.Sprintf("Recording #%d", recordingID),
					recordType: recordingType,
				}
			} else {
				title := generic.Title
				if title == "" {
					title = generic.Subject
				}
				if title == "" {
					title = fmt.Sprintf("%s #%d", recordingType, recordingID)
				}
				creator := ""
				if generic.Creator != nil {
					creator = generic.Creator.Name
				}
				data = detailData{
					title:      title,
					recordType: strings.Title(recordingType), //nolint:staticcheck
					content:    generic.Content,
					creator:    creator,
					createdAt:  generic.CreatedAt,
				}
			}
		}

		// Fetch comments for the recording
		commentsResult, err := client.Comments().List(ctx, scope.ProjectID, recordingID, nil)
		if err == nil && len(commentsResult.Comments) > 0 {
			for _, c := range commentsResult.Comments {
				creator := ""
				if c.Creator != nil {
					creator = c.Creator.Name
				}
				data.comments = append(data.comments, detailComment{
					creator:   creator,
					createdAt: c.CreatedAt,
					content:   c.Content,
				})
			}
		}

		// Best-effort subscription state — default to false if fetch fails
		data.subscribed = fetchSubscriptionState(
			client.Subscriptions().Get(ctx, scope.ProjectID, recordingID),
		)

		return detailLoadedMsg{data: data}
	}
}

// fetchSubscriptionState extracts the subscribed flag from a Subscriptions().Get
// result. Returns false on any error or nil response (best-effort fallback).
func fetchSubscriptionState(sub *basecamp.Subscription, err error) bool {
	if err != nil || sub == nil {
		return false
	}
	return sub.Subscribed
}
