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
}

// detailLoadedMsg is sent when the recording detail is fetched.
type detailLoadedMsg struct {
	data detailData
	err  error
}

// Detail shows a single recording with its content and metadata.
type Detail struct {
	session *workspace.Session
	styles  *tui.Styles

	recordingID   int64
	recordingType string
	data          *detailData
	preview       *widget.Preview
	spinner       spinner.Model
	loading       bool

	// Inline comment composer
	composer   *widget.Composer
	composing  bool
	submitting bool

	width, height int
}

// NewDetail creates a detail view for a specific recording.
func NewDetail(session *workspace.Session, recordingID int64, recordingType string) *Detail {
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
	return v.composing
}

// IsModal implements workspace.ModalActive.
func (v *Detail) IsModal() bool {
	return v.composing
}

func (v *Detail) ShortHelp() []key.Binding {
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "scroll")),
		key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comment")),
	}
	if v.composing {
		bindings = append(bindings,
			key.NewBinding(key.WithKeys("ctrl+enter"), key.WithHelp("ctrl+enter", "post comment")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		)
	}
	return bindings
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
	if v.composing {
		return v.handleComposingKey(msg)
	}

	switch msg.String() {
	case "c":
		v.composing = true
		v.relayout()
		return v.composer.Focus()
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

	return lipgloss.NewStyle().Padding(0, 1).Render(v.preview.View())
}

func (v *Detail) syncPreview() {
	if v.data == nil {
		return
	}

	v.preview.SetTitle(v.data.title)

	var fields []widget.PreviewField
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

		return detailLoadedMsg{data: data}
	}
}
