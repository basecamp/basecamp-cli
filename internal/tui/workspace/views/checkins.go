package views

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/empty"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// checkinsPane tracks which panel has focus.
type checkinsPane int

const (
	checkinsPaneLeft  checkinsPane = iota // questions
	checkinsPaneRight                     // answers
)

// checkinAnswerCreatedMsg is sent after posting a new check-in answer.
type checkinAnswerCreatedMsg struct {
	questionID int64
	submitID   uint64 // generation counter to detect stale completions
	err        error
}

// Checkins is the split-pane view for check-in questions and their answers.
type Checkins struct {
	session *workspace.Session
	pool    *data.Pool[[]data.CheckinQuestionInfo]
	styles  *tui.Styles

	// Layout
	split          *widget.SplitPane
	listQuestions  *widget.List // left pane
	listAnswers    *widget.List // right pane
	focus          checkinsPane
	spinner        spinner.Model
	loading        bool
	loadingAnswers bool
	width, height  int

	// Data
	questions          []data.CheckinQuestionInfo
	answers            []data.CheckinAnswerInfo
	selectedQuestionID int64

	// Composer
	answering    bool
	submitting   bool // true while create answer is in-flight
	submitID     uint64
	submitCancel context.CancelFunc
	composer     *widget.Composer
}

// NewCheckins creates the check-ins view.
func NewCheckins(session *workspace.Session) *Checkins {
	styles := session.Styles()
	scope := session.Scope()
	pool := session.Hub().Checkins(scope.ProjectID, scope.ToolID)

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	listQuestions := widget.NewList(styles)
	listQuestions.SetEmptyMessage(empty.NoCheckins())
	listQuestions.SetFocused(true)

	listAnswers := widget.NewList(styles)
	listAnswers.SetEmptyText("Select a question to view answers.")
	listAnswers.SetFocused(false)

	split := widget.NewSplitPane(styles, 0.35)

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

	composer := widget.NewComposer(styles,
		widget.WithMode(widget.ComposerRich),
		widget.WithAutoExpand(false),
		widget.WithUploadFn(uploadFn),
		widget.WithContext(session.Context()),
		widget.WithPlaceholder("Your answer (Markdown)..."),
	)

	return &Checkins{
		session:       session,
		pool:          pool,
		styles:        styles,
		split:         split,
		listQuestions: listQuestions,
		listAnswers:   listAnswers,
		focus:         checkinsPaneLeft,
		spinner:       s,
		loading:       true,
		composer:      composer,
	}
}

// Title implements View.
func (v *Checkins) Title() string {
	return "Check-ins"
}

// HasSplitPane implements workspace.SplitPaneFocuser.
func (v *Checkins) HasSplitPane() bool { return true }

// ShortHelp implements View.
func (v *Checkins) ShortHelp() []key.Binding {
	if v.listQuestions.Filtering() || v.listAnswers.Filtering() {
		return filterHints()
	}
	if v.answering {
		return []key.Binding{
			key.NewBinding(key.WithKeys("ctrl+enter"), key.WithHelp("ctrl+enter", "send")),
			key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "$EDITOR")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	}
	if v.focus == checkinsPaneLeft {
		return []key.Binding{
			key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch pane")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		}
	}
	hints := []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "switch pane")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
	if v.selectedQuestionID != 0 {
		hints = append(hints, key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new answer")))
	}
	return hints
}

// FullHelp implements View.
func (v *Checkins) FullHelp() [][]key.Binding {
	return [][]key.Binding{v.ShortHelp()}
}

// StartFilter implements workspace.Filterable.
func (v *Checkins) StartFilter() {
	if v.focus == checkinsPaneLeft {
		v.listQuestions.StartFilter()
	} else {
		v.listAnswers.StartFilter()
	}
}

// InputActive implements workspace.InputCapturer.
func (v *Checkins) InputActive() bool {
	return v.listQuestions.Filtering() || v.listAnswers.Filtering() || v.answering
}

// IsModal implements workspace.ModalActive.
func (v *Checkins) IsModal() bool {
	return v.answering
}

// SetSize implements View.
func (v *Checkins) SetSize(w, h int) {
	v.width = w
	v.height = h
	v.split.SetSize(w, h)
	v.listQuestions.SetSize(v.split.LeftWidth(), h)
	v.listAnswers.SetSize(v.split.RightWidth(), h)
}

// Init implements tea.Model.
func (v *Checkins) Init() tea.Cmd {
	snap := v.pool.Get()
	if snap.Usable() {
		v.questions = snap.Data
		v.syncQuestions()
		v.loading = false
		if snap.Fresh() {
			if item := v.listQuestions.Selected(); item != nil {
				return v.selectQuestion(item.ID)
			}
			return nil
		}
	}
	return tea.Batch(v.spinner.Tick, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
}

// Update implements tea.Model.
func (v *Checkins) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workspace.FocusMsg:
		cmds := []tea.Cmd{v.pool.FetchIfStale(v.session.Hub().ProjectContext())}
		if v.selectedQuestionID != 0 {
			answersPool := v.session.Hub().CheckinAnswers(v.session.Scope().ProjectID, v.selectedQuestionID)
			cmds = append(cmds, answersPool.FetchIfStale(v.session.Hub().ProjectContext()))
		}
		return v, tea.Batch(cmds...)

	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				v.questions = snap.Data
				v.syncQuestions()
				v.loading = false
				if v.selectedQuestionID == 0 {
					if item := v.listQuestions.Selected(); item != nil {
						return v, v.selectQuestion(item.ID)
					}
				}
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading check-in questions")
			}
			if snap.Loading() && !snap.HasData {
				v.loading = true
			}
		} else if v.selectedQuestionID != 0 {
			answersPool := v.session.Hub().CheckinAnswers(v.session.Scope().ProjectID, v.selectedQuestionID)
			if msg.Key == answersPool.Key() {
				snap := answersPool.Get()
				if snap.Usable() {
					v.loadingAnswers = false
					v.syncAnswers(v.selectedQuestionID, snap.Data)
				}
				if snap.State == data.StateError {
					v.loadingAnswers = false
					return v, workspace.ReportError(snap.Err, "loading answers")
				}
			}
		}
		return v, nil

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

	case checkinAnswerCreatedMsg:
		// Ignore stale completions from a previous (canceled) request
		if msg.submitID != v.submitID {
			return v, nil
		}
		v.submitting = false
		v.submitCancel = nil
		if msg.err != nil {
			// Silently absorb cancellation — draft stays open, no error toast
			if errors.Is(msg.err, context.Canceled) {
				return v, nil
			}
			// Keep composer open with draft intact so the user can retry
			return v, workspace.ReportError(msg.err, "creating answer")
		}
		v.answering = false
		v.composer.Reset()
		answersPool := v.session.Hub().CheckinAnswers(v.session.Scope().ProjectID, msg.questionID)
		answersPool.Invalidate()
		return v, tea.Batch(
			answersPool.Fetch(v.session.Hub().ProjectContext()),
			workspace.SetStatus("Answer posted", false),
		)

	case widget.ComposerSubmitMsg:
		if msg.Err != nil {
			return v, workspace.ReportError(msg.Err, "composing answer")
		}
		v.submitting = true
		return v, tea.Batch(v.spinner.Tick, v.createAnswer(msg.Content))

	case widget.EditorReturnMsg:
		if v.answering {
			return v, v.composer.HandleEditorReturn(msg)
		}

	case widget.AttachFileRequestMsg:
		if v.answering {
			return v, workspace.SetStatus("Paste a file path or drag a file into the terminal", false)
		}

	case spinner.TickMsg:
		if v.loading || v.loadingAnswers || v.submitting {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		if v.answering {
			return v, v.handleAnsweringKey(msg)
		}
		if v.loading {
			return v, nil
		}
		return v, v.handleKey(msg)
	}

	// Forward non-key messages to composer when answering
	if v.answering {
		if cmd := v.composer.Update(msg); cmd != nil {
			return v, cmd
		}
	}

	return v, nil
}

func (v *Checkins) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Filter guard: forward all keys to focused list during filter
	if v.listQuestions.Filtering() || v.listAnswers.Filtering() {
		return v.updateFocusedList(msg)
	}

	listKeys := workspace.DefaultListKeyMap()

	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("tab", "shift+tab"))):
		v.toggleFocus()
		return nil

	case key.Matches(msg, key.NewBinding(key.WithKeys("n"))):
		if v.focus == checkinsPaneRight && v.selectedQuestionID != 0 {
			v.answering = true
			v.composer.Reset()
			v.composer.SetSize(v.split.RightWidth(), 6)
			return v.composer.Focus()
		}

	case key.Matches(msg, listKeys.Open):
		if v.focus == checkinsPaneRight {
			return v.openSelectedAnswer()
		}
		// Enter on left pane explicitly selects the current question
		if item := v.listQuestions.Selected(); item != nil {
			return v.selectQuestion(item.ID)
		}
		return nil

	default:
		return v.updateFocusedList(msg)
	}
	return nil
}

func (v *Checkins) handleAnsweringKey(msg tea.KeyMsg) tea.Cmd {
	if v.submitting {
		// Only allow esc to cancel the in-flight request; block everything else
		if msg.String() == "esc" {
			if v.submitCancel != nil {
				v.submitCancel()
				v.submitCancel = nil
			}
			v.submitting = false
			return workspace.SetStatus("Canceled", false)
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		v.answering = false
		v.composer.Blur()
		v.composer.Reset()
		return nil
	default:
		return v.composer.Update(msg)
	}
}

func (v *Checkins) toggleFocus() {
	if v.focus == checkinsPaneLeft {
		v.focus = checkinsPaneRight
	} else {
		v.focus = checkinsPaneLeft
	}
	v.listQuestions.SetFocused(v.focus == checkinsPaneLeft)
	v.listAnswers.SetFocused(v.focus == checkinsPaneRight)
}

func (v *Checkins) updateFocusedList(msg tea.KeyMsg) tea.Cmd {
	if v.focus == checkinsPaneLeft {
		prevIdx := v.listQuestions.SelectedIndex()
		cmd := v.listQuestions.Update(msg)

		// If cursor moved, load the newly selected question's answers
		if v.listQuestions.SelectedIndex() != prevIdx {
			if item := v.listQuestions.Selected(); item != nil {
				return tea.Batch(cmd, v.selectQuestion(item.ID))
			}
		}
		return cmd
	}
	return v.listAnswers.Update(msg)
}

func (v *Checkins) selectQuestion(id string) tea.Cmd {
	var questionID int64
	fmt.Sscanf(id, "%d", &questionID)
	if questionID == v.selectedQuestionID {
		return nil
	}
	v.selectedQuestionID = questionID
	return v.loadAnswers(questionID)
}

func (v *Checkins) loadAnswers(questionID int64) tea.Cmd {
	answersPool := v.session.Hub().CheckinAnswers(v.session.Scope().ProjectID, questionID)
	snap := answersPool.Get()
	if snap.Usable() {
		v.loadingAnswers = false
		v.syncAnswers(questionID, snap.Data)
		if snap.Fresh() {
			return nil
		}
		// Stale but usable: keep showing cached answers while revalidating
		return answersPool.FetchIfStale(v.session.Hub().ProjectContext())
	}

	// No usable cache — show spinner
	v.loadingAnswers = true
	v.listAnswers.SetItems(nil)
	return tea.Batch(v.spinner.Tick, answersPool.FetchIfStale(v.session.Hub().ProjectContext()))
}

func (v *Checkins) openSelectedAnswer() tea.Cmd {
	item := v.listAnswers.Selected()
	if item == nil {
		return nil
	}
	var answerID int64
	fmt.Sscanf(item.ID, "%d", &answerID)

	// Record in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          item.ID,
			Title:       item.Title,
			Description: "Answer",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.session.Scope().ProjectID),
		})
	}

	scope := v.session.Scope()
	scope.RecordingID = answerID
	scope.RecordingType = "Question::Answer"
	return workspace.Navigate(workspace.ViewDetail, scope)
}

func (v *Checkins) createAnswer(content widget.ComposerContent) tea.Cmd {
	scope := v.session.Scope()
	questionID := v.selectedQuestionID
	hub := v.session.Hub()
	ctx, cancel := context.WithCancel(hub.ProjectContext())
	v.submitID++
	id := v.submitID
	v.submitCancel = cancel

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

	return func() tea.Msg {
		err := hub.CreateCheckinAnswer(ctx, scope.AccountID, scope.ProjectID, questionID, html)
		return checkinAnswerCreatedMsg{questionID: questionID, submitID: id, err: err}
	}
}

// View implements tea.Model.
func (v *Checkins) View() string {
	if v.loading {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading check-ins...")
	}

	left := v.listQuestions.View()

	var right string
	if v.loadingAnswers {
		right = lipgloss.NewStyle().
			Padding(0, 1).
			Render(v.spinner.View() + " Loading answers...")
	} else {
		right = v.renderRightPanel()
	}

	v.split.SetContent(left, right)
	return v.split.View()
}

func (v *Checkins) renderRightPanel() string {
	var b strings.Builder
	b.WriteString(v.listAnswers.View())

	if v.answering {
		b.WriteString("\n")
		theme := v.styles.Theme()
		sep := lipgloss.NewStyle().Foreground(theme.Border).Render("─ New Answer ─")
		b.WriteString(sep + "\n")
		if v.submitting {
			b.WriteString(v.spinner.View() + " Posting answer...")
		} else {
			b.WriteString(v.composer.View())
		}
	}

	return b.String()
}

// -- Data sync

func (v *Checkins) syncQuestions() {
	items := make([]widget.ListItem, 0, len(v.questions))
	for _, q := range v.questions {
		var parts []string
		if q.Frequency != "" {
			parts = append(parts, formatFrequency(q.Frequency))
		}
		if q.AnswersCount > 0 {
			parts = append(parts, fmt.Sprintf("%d answers", q.AnswersCount))
		}
		if q.Paused {
			parts = append(parts, "paused")
		}

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", q.ID),
			Title:       q.Title,
			Description: strings.Join(parts, " - "),
		})
	}
	v.listQuestions.SetItems(items)

	// Reconcile cursor with selectedQuestionID after list reorder/removal
	if v.selectedQuestionID != 0 {
		selectedID := fmt.Sprintf("%d", v.selectedQuestionID)
		if !v.listQuestions.SelectByID(selectedID) {
			// Selected question was removed — clear right pane
			v.selectedQuestionID = 0
			v.answers = nil
			v.listAnswers.SetItems(nil)
		}
	}
}

func (v *Checkins) syncAnswers(questionID int64, answers []data.CheckinAnswerInfo) {
	if questionID != v.selectedQuestionID {
		return
	}
	v.answers = answers
	items := make([]widget.ListItem, 0, len(answers))
	for _, a := range answers {
		title := a.Creator + " (" + a.CreatedAt.Format("Jan 2") + ")"
		desc := truncateContent(richtext.HTMLToMarkdown(a.Content), 80)

		items = append(items, widget.ListItem{
			ID:          fmt.Sprintf("%d", a.ID),
			Title:       title,
			Description: desc,
		})
	}
	v.listAnswers.SetItems(items)
}

// truncateContent returns the first line of content, truncated to maxLen.
func truncateContent(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

func formatFrequency(freq string) string {
	switch freq {
	case "every_day":
		return "Daily"
	case "every_week":
		return "Weekly"
	case "every_other_week":
		return "Biweekly"
	case "every_month":
		return "Monthly"
	case "on_certain_days":
		return "Certain days"
	default:
		return freq
	}
}
