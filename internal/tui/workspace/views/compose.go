package views

import (
	"context"
	"io"
	"os"
	"strings"

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

// composeFocus tracks whether the subject or body has focus.
type composeFocus int

const (
	composeFocusSubject composeFocus = iota
	composeFocusBody
)

// composeKeyMap defines compose-specific keybindings.
type composeKeyMap struct {
	Send      key.Binding
	SwitchTab key.Binding
	Cancel    key.Binding
}

func defaultComposeKeyMap() composeKeyMap {
	return composeKeyMap{
		Send: key.NewBinding(
			key.WithKeys("ctrl+enter"),
			key.WithHelp("ctrl+enter", "send"),
		),
		SwitchTab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch field"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

// Compose is a general-purpose compose view for creating messages.
type Compose struct {
	session *workspace.Session
	styles  *tui.Styles
	keys    composeKeyMap

	subject  textinput.Model
	composer *widget.Composer
	focus    composeFocus

	// What we're composing
	composeType workspace.ComposeType
	projectID   int64
	boardID     int64

	spinner       spinner.Model
	width, height int
	sending       bool
}

// NewCompose creates a new compose view.
func NewCompose(session *workspace.Session) *Compose {
	styles := session.Styles()
	scope := session.Scope()

	subj := textinput.New()
	subj.Placeholder = "Subject"
	subj.CharLimit = 256
	subj.Focus()

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
		widget.WithPlaceholder("Message body (Markdown supported)..."),
	)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	return &Compose{
		session:     session,
		styles:      styles,
		keys:        defaultComposeKeyMap(),
		subject:     subj,
		composer:    comp,
		focus:       composeFocusSubject,
		composeType: workspace.ComposeMessage,
		projectID:   scope.ProjectID,
		boardID:     scope.ToolID,
		spinner:     s,
	}
}

// Title implements View.
func (v *Compose) Title() string {
	return "New Message"
}

// InputActive implements workspace.InputCapturer.
func (v *Compose) InputActive() bool {
	return true
}

// IsModal implements workspace.ModalActive.
func (v *Compose) IsModal() bool {
	return true
}

// ShortHelp implements View.
func (v *Compose) ShortHelp() []key.Binding {
	return []key.Binding{v.keys.Send, v.keys.SwitchTab, v.keys.Cancel}
}

// FullHelp implements View.
func (v *Compose) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// SetSize implements View.
func (v *Compose) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.subject.Width = max(0, w-4)
	// Subject takes 2 lines (label + input), body gets the rest
	bodyHeight := h - 4 // subject label + input + separator + padding
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	v.composer.SetSize(w, bodyHeight)
}

// Init implements tea.Model.
func (v *Compose) Init() tea.Cmd {
	if v.boardID == 0 {
		if id := v.findMessageBoardID(); id != 0 {
			v.boardID = id
		} else {
			return workspace.SetStatus("No message board in this project", true)
		}
	}
	return tea.Batch(textinput.Blink, v.spinner.Tick)
}

// findMessageBoardID scans the projects pool for the current project's dock
// tools and returns the ID of the message_board tool, or 0 if not found.
func (v *Compose) findMessageBoardID() int64 {
	snap := v.session.Hub().Projects().Get()
	if !snap.Usable() {
		return 0
	}
	for _, p := range snap.Data {
		if p.ID == v.projectID {
			for _, tool := range p.Dock {
				if tool.Name == "message_board" {
					return tool.ID
				}
			}
			return 0
		}
	}
	return 0
}

// Update implements tea.Model.
func (v *Compose) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.MessageCreatedMsg:
		v.sending = false
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "posting message")
		}
		return v, tea.Batch(
			workspace.NavigateBack(),
			workspace.SetStatus("Message posted", false),
			func() tea.Msg { return workspace.RefreshMsg{} },
		)

	case widget.ComposerSubmitMsg:
		if msg.Err != nil {
			v.sending = false
			return v, workspace.ReportError(msg.Err, "composing message")
		}
		return v, v.postMessage(msg.Content)

	case widget.EditorReturnMsg:
		return v, v.composer.HandleEditorReturn(msg)

	case widget.AttachFileRequestMsg:
		return v, workspace.SetStatus("Paste a file path or drag a file into the terminal", false)

	case spinner.TickMsg:
		if v.sending {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		if v.sending {
			return v, nil
		}
		return v, v.handleKey(msg)
	}

	// Forward to composer for upload results, etc.
	if cmd := v.composer.Update(msg); cmd != nil {
		return v, cmd
	}

	return v, nil
}

func (v *Compose) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, v.keys.Cancel):
		return workspace.NavigateBack()

	case key.Matches(msg, v.keys.Send):
		return v.submit()

	case key.Matches(msg, v.keys.SwitchTab):
		return v.toggleFocus()

	case msg.Paste && v.focus == composeFocusBody:
		text, cmd := v.composer.ProcessPaste(string(msg.Runes))
		v.composer.InsertPaste(text)
		return cmd

	default:
		if v.focus == composeFocusSubject {
			var cmd tea.Cmd
			v.subject, cmd = v.subject.Update(msg)
			return cmd
		}
		return v.composer.Update(msg)
	}
}

func (v *Compose) toggleFocus() tea.Cmd {
	if v.focus == composeFocusSubject {
		v.focus = composeFocusBody
		v.subject.Blur()
		return v.composer.Focus()
	}
	v.focus = composeFocusSubject
	v.composer.Blur()
	v.subject.Focus()
	return textinput.Blink
}

func (v *Compose) submit() tea.Cmd {
	subject := v.subject.Value()
	if subject == "" {
		return workspace.SetStatus("Subject is required", true)
	}
	cmd := v.composer.Submit()
	if cmd == nil {
		// Composer had no content — don't lock up
		return workspace.SetStatus("Message body is required", true)
	}
	v.sending = true
	return tea.Batch(v.spinner.Tick, cmd)
}

func (v *Compose) postMessage(content widget.ComposerContent) tea.Cmd {
	subject := v.subject.Value()
	projectID := v.projectID
	boardID := v.boardID

	var html string
	if content.Markdown != "" {
		html = richtext.MarkdownToHTML(content.Markdown)
	}
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

	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		msg, err := client.Messages().Create(ctx, projectID, boardID, &basecamp.CreateMessageRequest{
			Subject: subject,
			Content: html,
		})
		if err != nil {
			return workspace.MessageCreatedMsg{Err: err}
		}
		creator := ""
		if msg.Creator != nil {
			creator = msg.Creator.Name
		}
		return workspace.MessageCreatedMsg{
			Message: workspace.MessageInfo{
				ID:      msg.ID,
				Subject: msg.Subject,
				Creator: creator,
			},
		}
	}
}

// View implements tea.Model.
func (v *Compose) View() string {
	if v.width == 0 || v.height == 0 {
		return ""
	}

	if v.sending {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Posting message…")
	}

	theme := v.styles.Theme()
	labelStyle := lipgloss.NewStyle().Foreground(theme.Muted)

	sections := make([]string, 0, 5)

	// Subject
	focusLabel := ""
	if v.focus == composeFocusSubject {
		focusLabel = " *"
	}
	sections = append(sections, labelStyle.Render("Subject"+focusLabel))
	sections = append(sections, v.subject.View())

	// Separator
	sep := lipgloss.NewStyle().
		Width(v.width).
		Foreground(theme.Border).
		Render(strings.Repeat("─", max(1, v.width)))
	sections = append(sections, sep)

	// Body
	bodyLabel := ""
	if v.focus == composeFocusBody {
		bodyLabel = " *"
	}
	sections = append(sections, labelStyle.Render("Body"+bodyLabel))
	sections = append(sections, v.composer.View())

	return lipgloss.NewStyle().Padding(0, 1).Render(
		lipgloss.JoinVertical(lipgloss.Left, sections...),
	)
}
