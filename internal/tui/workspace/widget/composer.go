// Package widget provides reusable TUI components.
package widget

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// ComposerMode determines the input style.
type ComposerMode int

const (
	// ComposerQuick uses a single-line textinput; Enter sends.
	ComposerQuick ComposerMode = iota
	// ComposerRich uses a multi-line textarea; Enter inserts newline, ctrl+enter sends.
	ComposerRich
)

// AttachStatus tracks upload progress for a single attachment.
type AttachStatus int

const (
	AttachPending   AttachStatus = iota // queued, not yet uploading
	AttachUploading                     // upload in progress
	AttachUploaded                      // upload complete, SGID available
	AttachFailed                        // upload failed
)

// Attachment represents a file being attached to the composed content.
type Attachment struct {
	Path        string
	Filename    string
	ContentType string
	SGID        string       // populated after upload
	Status      AttachStatus // current upload state
	Err         error        // set when Status == AttachFailed
}

// UploadFunc is the callback for uploading a file. Called in a tea.Cmd goroutine.
// Returns the attachable SGID on success.
type UploadFunc func(ctx context.Context, path, filename, contentType string) (sgid string, err error)

// ComposerContent is the structured output from the composer.
type ComposerContent struct {
	Markdown    string
	Attachments []Attachment
	IsPlain     bool // true when no formatting and no attachments
}

// ComposerSubmitMsg is sent when the composer finishes processing a submission.
type ComposerSubmitMsg struct {
	Content ComposerContent
	Err     error
}

// AttachFileRequestMsg is sent when the user presses ctrl+f to attach a file.
// The containing view should display a hint (e.g. "Paste a file path or drag a file into the terminal").
type AttachFileRequestMsg struct{}

// attachUploadedMsg is sent when a single attachment upload completes.
type attachUploadedMsg struct {
	index int
	sgid  string
	err   error
}

// composerKeyMap defines composer-specific keybindings.
type composerKeyMap struct {
	Send       key.Binding
	CtrlSend   key.Binding
	Editor     key.Binding
	AttachFile key.Binding
	Preview    key.Binding
}

func defaultComposerKeyMap() composerKeyMap {
	return composerKeyMap{
		Send: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "send"),
		),
		CtrlSend: key.NewBinding(
			key.WithKeys("ctrl+enter"),
			key.WithHelp("ctrl+enter", "send"),
		),
		Editor: key.NewBinding(
			key.WithKeys("ctrl+e"),
			key.WithHelp("ctrl+e", "$EDITOR"),
		),
		AttachFile: key.NewBinding(
			key.WithKeys("ctrl+f"),
			key.WithHelp("ctrl+f", "attach file"),
		),
		Preview: key.NewBinding(
			key.WithKeys("ctrl+t"),
			key.WithHelp("ctrl+t", "preview"),
		),
	}
}

// Composer is a reusable Markdown editing widget with attachment support.
type Composer struct {
	// Input widgets
	textInput  textinput.Model
	textArea   textarea.Model
	mode       ComposerMode
	autoExpand bool // auto-switch quick→rich on markdown formatting

	// Attachments
	attachments  []Attachment
	attachCursor int // selected attachment index (-1 = none)
	uploading    int // count of in-flight uploads

	// Configuration
	styles   *tui.Styles
	keys     composerKeyMap
	width    int
	height   int
	focused  bool
	preview  bool // toggle: edit vs rendered preview
	uploadFn UploadFunc
	ctx      context.Context // cancellable context for upload operations
}

// ComposerOption configures a Composer.
type ComposerOption func(*Composer)

// WithMode sets the initial composer mode.
func WithMode(mode ComposerMode) ComposerOption {
	return func(c *Composer) { c.mode = mode }
}

// WithAutoExpand enables auto-switching from quick to rich mode.
func WithAutoExpand(auto bool) ComposerOption {
	return func(c *Composer) { c.autoExpand = auto }
}

// WithUploadFn sets the file upload callback.
func WithUploadFn(fn UploadFunc) ComposerOption {
	return func(c *Composer) { c.uploadFn = fn }
}

// WithContext sets the cancellable context for upload operations.
// Uploads started with this context will abort if the context is canceled
// (e.g., on account switch). Defaults to context.Background().
func WithContext(ctx context.Context) ComposerOption {
	return func(c *Composer) { c.ctx = ctx }
}

// WithPlaceholder sets the placeholder text for both input widgets.
func WithPlaceholder(text string) ComposerOption {
	return func(c *Composer) {
		c.textInput.Placeholder = text
		c.textArea.Placeholder = text
	}
}

// NewComposer creates a new Composer widget.
func NewComposer(styles *tui.Styles, opts ...ComposerOption) *Composer {
	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	ti.CharLimit = 0

	ta := textarea.New()
	ta.Placeholder = "Type a message... (ctrl+enter to send)"
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(3)

	c := &Composer{
		textInput:    ti,
		textArea:     ta,
		mode:         ComposerQuick,
		autoExpand:   true,
		attachCursor: -1,
		styles:       styles,
		keys:         defaultComposerKeyMap(),
		ctx:          context.Background(),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Focus gives the composer focus.
func (c *Composer) Focus() tea.Cmd {
	c.focused = true
	if c.mode == ComposerQuick {
		c.textInput.Focus()
		return textinput.Blink
	}
	c.textArea.Focus()
	return textarea.Blink
}

// Blur removes focus from the composer.
func (c *Composer) Blur() {
	c.focused = false
	c.textInput.Blur()
	c.textArea.Blur()
}

// InputActive returns true if the composer is capturing text input.
func (c *Composer) InputActive() bool {
	return c.focused
}

// Mode returns the current composer mode.
func (c *Composer) Mode() ComposerMode {
	return c.mode
}

// SetSize updates the composer dimensions.
func (c *Composer) SetSize(w, h int) {
	c.width = w
	c.height = h
	c.textInput.Width = max(0, w-4)
	c.textArea.SetWidth(max(0, w-2))
	if h > 2 {
		c.textArea.SetHeight(h - c.attachBarHeight())
	}
}

// Value returns the current text content.
func (c *Composer) Value() string {
	if c.mode == ComposerQuick {
		return c.textInput.Value()
	}
	return c.textArea.Value()
}

// SetValue sets the text content (useful for pre-populating).
func (c *Composer) SetValue(s string) {
	if c.mode == ComposerQuick {
		c.textInput.SetValue(s)
	} else {
		c.textArea.SetValue(s)
	}
}

// InsertPaste appends pasted text at the current cursor position.
// If the text contains newlines or markdown, the composer auto-expands to rich mode.
func (c *Composer) InsertPaste(text string) {
	if text == "" {
		return
	}
	if c.mode == ComposerQuick && (strings.Contains(text, "\n") || richtext.IsMarkdown(text)) {
		c.expandToRich()
	}
	existing := c.Value()
	c.SetValue(existing + text)
}

// Reset clears all content and attachments.
func (c *Composer) Reset() {
	c.textInput.Reset()
	c.textArea.Reset()
	c.attachments = nil
	c.attachCursor = -1
	c.uploading = 0
	c.preview = false
}

// Attachments returns the current attachment list.
func (c *Composer) Attachments() []Attachment {
	return c.attachments
}

// HasContent returns true if there is text or attachments.
func (c *Composer) HasContent() bool {
	return strings.TrimSpace(c.Value()) != "" || len(c.attachments) > 0
}

// AddAttachment queues a file for attachment and triggers upload if an upload function is set.
func (c *Composer) AddAttachment(path string) tea.Cmd {
	if err := richtext.ValidateFile(path); err != nil {
		return func() tea.Msg {
			return ComposerSubmitMsg{Err: err}
		}
	}

	filename := filepath.Base(path)
	contentType := richtext.DetectMIME(path)

	att := Attachment{
		Path:        path,
		Filename:    filename,
		ContentType: contentType,
		Status:      AttachPending,
	}
	c.attachments = append(c.attachments, att)
	idx := len(c.attachments) - 1

	// Auto-expand to rich mode when attaching files
	if c.mode == ComposerQuick {
		c.expandToRich()
	}

	// Start upload if upload function is available
	if c.uploadFn != nil {
		c.attachments[idx].Status = AttachUploading
		c.uploading++
		return c.startUpload(idx)
	}
	return nil
}

// Submit creates a tea.Cmd that uploads any pending attachments and returns ComposerSubmitMsg.
func (c *Composer) Submit() tea.Cmd {
	content := strings.TrimSpace(c.Value())
	if content == "" && len(c.attachments) == 0 {
		return nil
	}

	attachments := make([]Attachment, len(c.attachments))
	copy(attachments, c.attachments)
	uploadFn := c.uploadFn
	ctx := c.ctx

	return func() tea.Msg {
		// Upload any pending attachments
		for i := range attachments {
			if attachments[i].Status == AttachPending && uploadFn != nil {
				attachments[i].Status = AttachUploading
				sgid, err := uploadFn(
					ctx,
					attachments[i].Path,
					attachments[i].Filename,
					attachments[i].ContentType,
				)
				if err != nil {
					attachments[i].Status = AttachFailed
					attachments[i].Err = err
					return ComposerSubmitMsg{Err: fmt.Errorf("uploading %s: %w", attachments[i].Filename, err)}
				}
				attachments[i].SGID = sgid
				attachments[i].Status = AttachUploaded
			}
		}

		isPlain := !richtext.IsMarkdown(content) && len(attachments) == 0
		return ComposerSubmitMsg{
			Content: ComposerContent{
				Markdown:    content,
				Attachments: attachments,
				IsPlain:     isPlain,
			},
		}
	}
}

// Update processes messages for the composer.
func (c *Composer) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case attachUploadedMsg:
		return c.handleUploadResult(msg)
	case tea.KeyMsg:
		return c.handleKey(msg)
	}
	return nil
}

func (c *Composer) handleKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, c.keys.Editor):
		return c.OpenEditor()

	case key.Matches(msg, c.keys.AttachFile):
		return func() tea.Msg { return AttachFileRequestMsg{} }

	case key.Matches(msg, c.keys.Preview) && c.mode == ComposerRich:
		c.preview = !c.preview
		return nil
	}

	if c.mode == ComposerQuick {
		return c.handleQuickKey(msg)
	}
	return c.handleRichKey(msg)
}

func (c *Composer) handleQuickKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, c.keys.Send):
		return c.Submit()
	default:
		// Check for auto-expand triggers before forwarding to textinput
		if c.autoExpand && shouldExpand(msg) {
			c.expandToRich()
			// Forward the key to the textarea
			var cmd tea.Cmd
			c.textArea, cmd = c.textArea.Update(msg)
			return cmd
		}
		var cmd tea.Cmd
		c.textInput, cmd = c.textInput.Update(msg)
		return cmd
	}
}

func (c *Composer) handleRichKey(msg tea.KeyMsg) tea.Cmd {
	if c.preview {
		// In preview mode, only send and toggle-back work
		switch {
		case key.Matches(msg, c.keys.CtrlSend):
			return c.Submit()
		case key.Matches(msg, c.keys.Preview):
			c.preview = false
			return nil
		}
		return nil
	}

	switch {
	case key.Matches(msg, c.keys.CtrlSend):
		return c.Submit()
	default:
		var cmd tea.Cmd
		c.textArea, cmd = c.textArea.Update(msg)
		return cmd
	}
}

// shouldExpand returns true if the typed character is a markdown formatting trigger.
func shouldExpand(msg tea.KeyMsg) bool {
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		return false
	}
	switch msg.Runes[0] {
	case '*', '#', '`', '>', '~':
		return true
	}
	return false
}

func (c *Composer) expandToRich() {
	// Transfer content from textinput to textarea
	val := c.textInput.Value()
	c.textArea.SetValue(val)
	c.textArea.Focus()
	c.textInput.Blur()
	c.mode = ComposerRich
}

func (c *Composer) startUpload(idx int) tea.Cmd {
	att := c.attachments[idx]
	uploadFn := c.uploadFn
	ctx := c.ctx
	return func() tea.Msg {
		sgid, err := uploadFn(ctx, att.Path, att.Filename, att.ContentType)
		return attachUploadedMsg{index: idx, sgid: sgid, err: err}
	}
}

func (c *Composer) handleUploadResult(msg attachUploadedMsg) tea.Cmd {
	c.uploading--
	if msg.index < len(c.attachments) {
		if msg.err != nil {
			c.attachments[msg.index].Status = AttachFailed
			c.attachments[msg.index].Err = msg.err
		} else {
			c.attachments[msg.index].SGID = msg.sgid
			c.attachments[msg.index].Status = AttachUploaded
		}
	}
	return nil
}

// OpenEditor launches $EDITOR with the current content. The containing view
// must handle the returned EditorReturnMsg by calling HandleEditorReturn.
func (c *Composer) OpenEditor() tea.Cmd {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = "vi"
	}

	tmpFile, err := os.CreateTemp("", "compose-*.md")
	if err != nil {
		return func() tea.Msg {
			return EditorReturnMsg{Err: fmt.Errorf("creating temp file: %w", err)}
		}
	}

	// Pre-populate with current content
	content := c.Value()
	if content != "" {
		_, _ = tmpFile.WriteString(content)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	// Build the editor command — always use exec.Command to avoid shell injection
	parts := strings.Fields(editor)
	args := append(parts[1:], tmpPath)
	cmd := exec.Command(parts[0], args...) //nolint:gosec,noctx

	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			os.Remove(tmpPath)
			return EditorReturnMsg{Err: err}
		}
		data, readErr := os.ReadFile(tmpPath)
		os.Remove(tmpPath)
		if readErr != nil {
			return EditorReturnMsg{Err: readErr}
		}
		return EditorReturnMsg{Content: string(data)}
	})
}

// EditorReturnMsg is sent when the external editor process exits.
// Containing views must handle this and call HandleEditorReturn.
type EditorReturnMsg struct {
	Content string
	Err     error
}

// HandleEditorReturn populates the composer with the editor's output.
func (c *Composer) HandleEditorReturn(msg EditorReturnMsg) tea.Cmd {
	if msg.Err != nil {
		return func() tea.Msg {
			return ComposerSubmitMsg{Err: fmt.Errorf("editor: %w", msg.Err)}
		}
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return nil
	}
	// If content has multiple lines or markdown, switch to rich mode
	if strings.Contains(content, "\n") || richtext.IsMarkdown(content) {
		if c.mode == ComposerQuick {
			c.expandToRich()
		}
		c.textArea.SetValue(content)
	} else {
		c.SetValue(content)
	}
	return nil
}

// ProcessPaste handles pasted text, detecting file paths and adding them as attachments.
// Returns remaining text that should be inserted, and any commands to execute.
func (c *Composer) ProcessPaste(text string) (string, tea.Cmd) {
	lines := strings.Split(text, "\n")
	var textLines []string
	var cmds []tea.Cmd

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			textLines = append(textLines, line)
			continue
		}

		// Expand ~ to home directory
		expanded := trimmed
		if strings.HasPrefix(expanded, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = filepath.Join(home, expanded[2:])
			}
		}

		// Check if it's a file path
		info, err := os.Stat(expanded)
		if err == nil && info.Mode().IsRegular() {
			cmd := c.AddAttachment(expanded)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else {
			textLines = append(textLines, line)
		}
	}

	var batchCmd tea.Cmd
	if len(cmds) > 0 {
		batchCmd = tea.Batch(cmds...)
	}
	return strings.Join(textLines, "\n"), batchCmd
}

// View renders the composer.
func (c *Composer) View() string {
	if c.width == 0 {
		return ""
	}

	theme := c.styles.Theme()

	if c.preview && c.mode == ComposerRich {
		return c.renderPreview()
	}

	var sections []string

	// Input area
	if c.mode == ComposerQuick {
		sections = append(sections, c.textInput.View())
	} else {
		sections = append(sections, c.textArea.View())
	}

	// Attachment bar (Phase 8a: below the input)
	if len(c.attachments) > 0 {
		sections = append(sections, c.renderAttachBar(theme))
	}

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (c *Composer) renderAttachBar(theme tui.Theme) string {
	chips := make([]string, 0, len(c.attachments))
	for i, att := range c.attachments {
		chip := c.renderChip(i, att, theme)
		chips = append(chips, chip)
	}
	return strings.Join(chips, " ")
}

func (c *Composer) renderChip(idx int, att Attachment, theme tui.Theme) string {
	var icon string
	switch att.Status {
	case AttachPending:
		icon = "⏳"
	case AttachUploading:
		icon = "⏳"
	case AttachUploaded:
		icon = "✓"
	case AttachFailed:
		icon = "✗"
	}

	label := fmt.Sprintf("📎 %s %s", att.Filename, icon)

	style := lipgloss.NewStyle().
		Foreground(theme.Foreground).
		Background(theme.Background).
		Padding(0, 1)

	if att.Status == AttachFailed {
		style = style.Foreground(theme.Error)
	}
	if idx == c.attachCursor {
		style = style.Bold(true).Underline(true)
	}

	return style.Render(label)
}

func (c *Composer) renderPreview() string {
	content := c.textArea.Value()
	rendered, err := richtext.RenderMarkdownWithWidth(content, max(10, c.width-4))
	if err != nil {
		rendered = content
	}

	theme := c.styles.Theme()
	header := lipgloss.NewStyle().
		Foreground(theme.Muted).
		Render("── Preview (ctrl+t to edit) ──")

	return lipgloss.JoinVertical(lipgloss.Left, header, rendered)
}

func (c *Composer) attachBarHeight() int {
	if len(c.attachments) == 0 {
		return 0
	}
	return 1
}

// ShortHelp returns context-appropriate key bindings for the status bar.
func (c *Composer) ShortHelp() []key.Binding {
	if c.mode == ComposerQuick {
		return []key.Binding{c.keys.Send, c.keys.Editor}
	}
	return []key.Binding{c.keys.CtrlSend, c.keys.Editor, c.keys.AttachFile}
}
