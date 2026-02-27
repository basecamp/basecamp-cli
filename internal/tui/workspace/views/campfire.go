package views

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// campfireMode tracks whether the user is typing or scrolling.
type campfireMode int

const (
	campfireModeInput campfireMode = iota
	campfireModeScroll
)

// campfireKeyMap defines campfire-specific keybindings.
type campfireKeyMap struct {
	EnterInput   key.Binding
	ScrollMode   key.Binding
	ScrollUp     key.Binding
	ScrollDown   key.Binding
	ScrollTop    key.Binding
	ScrollBottom key.Binding
}

func defaultCampfireKeyMap() campfireKeyMap {
	return campfireKeyMap{
		EnterInput: key.NewBinding(
			key.WithKeys("i"),
			key.WithHelp("i", "input"),
		),
		ScrollMode: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "scroll"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("k", "up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("j", "down"),
		),
		ScrollTop: key.NewBinding(
			key.WithKeys("g"),
		),
		ScrollBottom: key.NewBinding(
			key.WithKeys("G"),
		),
	}
}

// pendingLine is a locally-appended message awaiting API confirmation.
type pendingLine struct {
	content string
	isHTML  bool
	sentAt  time.Time
}

// Campfire is the chat stream view for a project campfire.
type Campfire struct {
	session *workspace.Session
	pool    *data.Pool[data.CampfireLinesResult]
	styles  *tui.Styles
	keys    campfireKeyMap

	// IDs
	projectID  int64
	campfireID int64

	// Layout
	width, height     int
	lastRenderedWidth int // track width for re-render on resize
	viewport          viewport.Model
	composer          *widget.Composer
	mode              campfireMode

	// Data
	lines          []workspace.CampfireLineInfo
	pending        []pendingLine
	lastID         int64 // highest line ID seen, for detecting new lines
	selectedLineID int64 // the line currently selected for boost/action

	// Pagination
	totalCount  int  // total lines available (from X-Total-Count)
	currentPage int  // last page loaded (1-based)
	hasMore     bool // whether older messages exist
	loadingMore bool // currently fetching older page

	// Loading
	spinner spinner.Model
	loading bool
}

// NewCampfire creates the campfire chat view.
func NewCampfire(session *workspace.Session) *Campfire {
	styles := session.Styles()
	scope := session.Scope()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true

	pool := session.Hub().CampfireLines(scope.ProjectID, scope.ToolID)

	// Create upload function for the composer — capture client at construction time.
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
		widget.WithMode(widget.ComposerQuick),
		widget.WithAutoExpand(true),
		widget.WithUploadFn(uploadFn),
		widget.WithContext(session.Context()),
		widget.WithPlaceholder("Type a message..."),
	)

	return &Campfire{
		session:     session,
		pool:        pool,
		styles:      styles,
		keys:        defaultCampfireKeyMap(),
		projectID:   scope.ProjectID,
		campfireID:  scope.ToolID,
		viewport:    vp,
		composer:    comp,
		mode:        campfireModeInput,
		spinner:     s,
		loading:     true,
		currentPage: 1,
	}
}

// Title implements View.
func (v *Campfire) Title() string {
	return "Campfire"
}

// InputActive implements workspace.InputCapturer.
func (v *Campfire) InputActive() bool {
	return v.mode == campfireModeInput
}

// ShortHelp implements View.
func (v *Campfire) ShortHelp() []key.Binding {
	if v.mode == campfireModeScroll {
		return []key.Binding{
			v.keys.EnterInput,
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "scroll")),
			key.NewBinding(key.WithKeys("b", "B"), key.WithHelp("b", "boost")),
		}
	}
	// Input mode
	composerHelp := v.composer.ShortHelp()
	bindings := make([]key.Binding, 0, 1+len(composerHelp))
	bindings = append(bindings, v.keys.ScrollMode)
	bindings = append(bindings, composerHelp...)
	return bindings
}

// FullHelp implements View.
func (v *Campfire) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{
			key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "scroll")),
			key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "compose")),
		},
		{
			key.NewBinding(key.WithKeys("b", "B"), key.WithHelp("b", "boost")),
		},
	}
}

// SetSize implements View.
func (v *Campfire) SetSize(w, h int) {
	widthChanged := w != v.width
	v.width = w
	v.height = h
	v.resizeViewport()
	if widthChanged && len(v.lines) > 0 {
		v.renderMessages()
	}
}

// Init implements tea.Model.
func (v *Campfire) Init() tea.Cmd {
	// Record campfire visit in recents
	if r := v.session.Recents(); r != nil {
		r.Add(recents.Item{
			ID:          fmt.Sprintf("%d", v.campfireID),
			Title:       "Campfire",
			Description: "Campfire",
			Type:        recents.TypeRecording,
			AccountID:   v.session.Scope().AccountID,
			ProjectID:   fmt.Sprintf("%d", v.projectID),
		})
	}

	cmds := []tea.Cmd{v.spinner.Tick, v.composer.Focus()}

	snap := v.pool.Get()
	if snap.Usable() {
		v.lines = snap.Data.Lines
		v.totalCount = snap.Data.TotalCount
		v.hasMore = v.totalCount > len(v.lines)
		v.updateLastID()
		// Default to most recent line for boost targeting
		if len(v.lines) > 0 {
			v.updateSelectedToLatest()
		}
		v.renderMessages()
		v.loading = false
	}
	if !snap.Fresh() {
		cmds = append(cmds, v.pool.FetchIfStale(v.session.Hub().ProjectContext()))
	}
	cmds = append(cmds, v.schedulePoll())
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (v *Campfire) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		if msg.Key == v.pool.Key() {
			snap := v.pool.Get()
			if snap.Usable() {
				// Detect new lines for hit/miss
				newHighest := v.lastID
				for _, line := range snap.Data.Lines {
					if line.ID > newHighest {
						newHighest = line.ID
					}
				}
				if newHighest > v.lastID {
					v.pool.RecordHit()
				} else if v.lastID > 0 {
					v.pool.RecordMiss()
				}
				v.lastID = newHighest

				v.lines = snap.Data.Lines
				v.totalCount = snap.Data.TotalCount
				v.hasMore = v.totalCount > len(v.lines)
				v.reconcilePending()
				// Update boost target to latest line when new lines arrive
				if len(v.lines) > 0 {
					v.updateSelectedToLatest()
				}
				v.renderMessages()
				v.loading = false
			}
			if snap.State == data.StateError {
				v.loading = false
				return v, workspace.ReportError(snap.Err, "loading campfire")
			}
		}
		return v, nil

	case workspace.CampfireLinesLoadedMsg:
		// Only prepend case remains (from fetchOlderLines)
		if msg.Prepend {
			return v, v.handleOlderLinesLoaded(msg)
		}
		return v, nil

	case workspace.CampfireLineSentMsg:
		if msg.Err != nil {
			// Remove the last pending line on error
			if len(v.pending) > 0 {
				v.pending = v.pending[:len(v.pending)-1]
				v.renderMessages()
			}
			return v, workspace.ReportError(msg.Err, "sending message")
		}
		return v, nil

	case widget.ComposerSubmitMsg:
		return v, v.handleComposerSubmit(msg)

	case widget.EditorReturnMsg:
		return v, v.composer.HandleEditorReturn(msg)

	case widget.AttachFileRequestMsg:
		return v, workspace.SetStatus("Paste a file path or drag a file into the terminal", false)

	case workspace.RefreshMsg:
		v.pool.Invalidate()
		v.loading = true
		return v, tea.Batch(v.spinner.Tick, v.pool.Fetch(v.session.Hub().ProjectContext()))

	case workspace.BoostCreatedMsg:
		// Optimistically update the boost count in the lines
		if msg.Target.RecordingID != 0 {
			for i, line := range v.lines {
				if line.ID == msg.Target.RecordingID {
					v.lines[i].BoostsSummary.Count++
					break
				}
			}
			v.renderMessages()
		}
		return v, nil

	case data.PollMsg:
		if msg.Tag == v.pool.Key() {
			return v, tea.Batch(
				v.pool.FetchIfStale(v.session.Hub().ProjectContext()),
				v.schedulePoll(),
			)
		}

	case workspace.FocusMsg:
		v.pool.SetFocused(true)
		if v.mode == campfireModeInput {
			return v, v.composer.Focus()
		}
		return v, nil

	case workspace.BlurMsg:
		v.pool.SetFocused(false)
		v.composer.Blur()

	case spinner.TickMsg:
		if v.loading || v.loadingMore {
			var cmd tea.Cmd
			v.spinner, cmd = v.spinner.Update(msg)
			return v, cmd
		}

	case tea.KeyMsg:
		return v, v.handleKey(msg)
	}

	// Forward other messages to composer (upload results, etc.)
	if cmd := v.composer.Update(msg); cmd != nil {
		return v, cmd
	}

	return v, nil
}

func (v *Campfire) handleComposerSubmit(msg widget.ComposerSubmitMsg) tea.Cmd {
	if msg.Err != nil {
		return workspace.ReportError(msg.Err, "composing message")
	}

	content := msg.Content
	v.composer.Reset()
	// Restore focus after reset and recalculate layout (mode may have changed)
	v.composer.Focus()
	v.resizeViewport()

	if content.IsPlain {
		return v.sendLine(content.Markdown, false)
	}

	// Rich content: convert markdown to HTML and embed attachments
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
	return v.sendLine(html, true)
}

func (v *Campfire) handleOlderLinesLoaded(msg workspace.CampfireLinesLoadedMsg) tea.Cmd {
	v.loadingMore = false
	if msg.Err != nil {
		return workspace.ReportError(msg.Err, "loading older messages")
	}

	if len(msg.Lines) == 0 {
		v.hasMore = false
		v.renderMessages()
		return nil
	}

	// Prepend older lines (they arrive newest-first, we reverse them,
	// so they're oldest-first — same as our existing lines)
	v.lines = append(msg.Lines, v.lines...)
	v.hasMore = len(v.lines) < v.totalCount

	// Remember how many content lines we had before
	oldContentLines := strings.Count(v.viewport.View(), "\n") + 1
	v.renderMessages()
	// After prepending, keep the scroll position so the user sees the same content
	newContentLines := len(strings.Split(v.viewport.View(), "\n"))
	added := newContentLines - oldContentLines
	if added > 0 {
		v.viewport.SetYOffset(added)
	}
	return nil
}

func (v *Campfire) handleKey(msg tea.KeyMsg) tea.Cmd {
	if v.mode == campfireModeInput {
		return v.handleInputKey(msg)
	}
	return v.handleScrollKey(msg)
}

func (v *Campfire) handleInputKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, v.keys.ScrollMode):
		v.mode = campfireModeScroll
		v.composer.Blur()
		return nil

	case msg.Paste:
		text, cmd := v.composer.ProcessPaste(string(msg.Runes))
		v.composer.InsertPaste(text)
		if v.composer.Mode() != widget.ComposerQuick {
			v.resizeViewport()
			v.renderMessages()
		}
		return cmd

	default:
		// Forward to composer — it handles Enter (send), ctrl+enter, ctrl+e, etc.
		prevMode := v.composer.Mode()
		cmd := v.composer.Update(msg)
		// Recalculate layout if composer mode changed (e.g., auto-expand to rich)
		if v.composer.Mode() != prevMode {
			v.resizeViewport()
			v.renderMessages()
		}
		return cmd
	}
}

func (v *Campfire) handleScrollKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, v.keys.EnterInput):
		v.mode = campfireModeInput
		return v.composer.Focus()

	case key.Matches(msg, v.keys.ScrollUp):
		v.viewport.LineUp(1)
		// After scrolling, target the most recent visible message for boost
		v.updateSelectedToLatest()
		return v.maybeLoadMore()

	case key.Matches(msg, v.keys.ScrollDown):
		v.viewport.LineDown(1)
		v.updateSelectedToLatest()

	case key.Matches(msg, v.keys.ScrollTop):
		v.viewport.GotoTop()
		v.updateSelectedToLatest()
		return v.maybeLoadMore()

	case key.Matches(msg, v.keys.ScrollBottom):
		v.viewport.GotoBottom()
		v.updateSelectedToLatest()

	case msg.String() == "b" || msg.String() == "B":
		return v.boostSelectedLine()
	}
	return nil
}

func (v *Campfire) maybeLoadMore() tea.Cmd {
	if v.viewport.YOffset == 0 && v.hasMore && !v.loadingMore {
		v.loadingMore = true
		v.currentPage++
		v.renderMessages() // re-render to show "loading older..." indicator
		return tea.Batch(v.spinner.Tick, v.fetchOlderLines())
	}
	return nil
}

// updateSelectedToLatest sets selectedLineID to the most recent line.
// This is called after scroll navigation so that boost always targets
// the latest message in the stream (most common use case).
func (v *Campfire) updateSelectedToLatest() {
	if len(v.lines) > 0 {
		v.selectedLineID = v.lines[len(v.lines)-1].ID
	}
}

func (v *Campfire) boostSelectedLine() tea.Cmd {
	if len(v.lines) == 0 {
		return nil
	}
	// Use selectedLineID if set, otherwise default to the most recent line
	targetID := v.selectedLineID
	if targetID == 0 {
		targetID = v.lines[len(v.lines)-1].ID
	}
	return func() tea.Msg {
		return workspace.OpenBoostPickerMsg{
			Target: workspace.BoostTarget{
				ProjectID:   v.session.Scope().ProjectID,
				RecordingID: targetID,
				Title:       "Campfire line",
			},
		}
	}
}

func (v *Campfire) sendLine(content string, isHTML bool) tea.Cmd {
	// Optimistic: append a pending line immediately
	displayContent := content
	if isHTML {
		displayContent = richtext.HTMLToMarkdown(content)
	}
	v.pending = append(v.pending, pendingLine{
		content: displayContent,
		isHTML:  isHTML,
		sentAt:  time.Now(),
	})
	v.renderMessages()

	campfireID := v.campfireID
	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		var opts *basecamp.CreateLineOptions
		if isHTML {
			opts = &basecamp.CreateLineOptions{ContentType: "text/html"}
		}
		_, err := client.Campfires().CreateLine(ctx, campfireID, content, opts)
		return workspace.CampfireLineSentMsg{Err: err}
	}
}

// reconcilePending removes pending lines whose content now appears in the
// server-returned lines. This is a best-effort match by content string.
func (v *Campfire) reconcilePending() {
	if len(v.pending) == 0 {
		return
	}

	serverContent := make(map[string]bool, len(v.lines))
	for _, line := range v.lines {
		serverContent[line.Body] = true
		// Also index the markdown-converted version for matching
		serverContent[richtext.HTMLToMarkdown(line.Body)] = true
	}

	remaining := v.pending[:0]
	for _, p := range v.pending {
		if !serverContent[p.content] && !serverContent["<div>"+p.content+"</div>"] {
			remaining = append(remaining, p)
		}
	}
	v.pending = remaining
}

func (v *Campfire) renderMessages() {
	theme := v.styles.Theme()
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	timeStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	pendingStyle := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true)
	bodyWidth := v.viewport.Width - 2
	if bodyWidth < 20 {
		bodyWidth = 20
	}

	if len(v.lines) == 0 && len(v.pending) == 0 {
		v.viewport.SetContent(lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("No messages yet. Start the conversation!"))
		return
	}

	var b strings.Builder

	// Show loading indicator at top when fetching older messages
	if v.loadingMore {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("Loading older messages..."))
		b.WriteString("\n\n")
	} else if v.hasMore {
		b.WriteString(lipgloss.NewStyle().Foreground(theme.Muted).Render("↑ scroll up for older messages"))
		b.WriteString("\n\n")
	}

	dateStyle := lipgloss.NewStyle().Foreground(theme.Muted).Align(lipgloss.Center).Width(bodyWidth)

	for i, line := range v.lines {
		// Date separator when the day changes
		dayChanged := false
		if i == 0 || !sameDay(v.lines[i-1].CreatedAtTS, line.CreatedAtTS) {
			if !line.CreatedAtTS.IsZero() {
				dayChanged = true
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(dateStyle.Render("── " + formatMessageDate(line.CreatedAtTS) + " ──"))
				b.WriteString("\n")
			}
		}

		// Group consecutive messages from same sender within 5 minutes,
		// but always show header after a date boundary.
		showHeader := true
		if i > 0 && !dayChanged {
			prev := v.lines[i-1]
			if prev.Creator == line.Creator && sameTimeGroup(prev.CreatedAtTS, line.CreatedAtTS) {
				showHeader = false
			}
		}

		if showHeader {
			// Add spacing before header, unless a date separator was just written
			if i > 0 && !dayChanged {
				b.WriteString("\n")
			}
			b.WriteString(nameStyle.Render(line.Creator))
			b.WriteString("  ")
			b.WriteString(timeStyle.Render(line.CreatedAt))
			if line.GetBoosts().Count > 0 {
				b.WriteString(lipgloss.NewStyle().Foreground(theme.Success).Render(fmt.Sprintf("  [♥ %d]", line.GetBoosts().Count)))
			}
			b.WriteString("\n")
		}

		body := richtext.HTMLToMarkdown(line.Body)
		rendered := wrapText(body, bodyWidth)
		b.WriteString(rendered)
		// Show boosts inline for grouped (non-header) messages
		if !showHeader && line.GetBoosts().Count > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(theme.Success).Render(fmt.Sprintf("  [♥ %d]", line.GetBoosts().Count)))
		}
		b.WriteString("\n")
	}

	// Render pending lines
	for _, p := range v.pending {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(nameStyle.Render("You"))
		b.WriteString("  ")
		b.WriteString(pendingStyle.Render("sending..."))
		b.WriteString("\n")
		b.WriteString(wrapText(p.content, bodyWidth))
		b.WriteString("\n")
	}

	v.viewport.SetContent(b.String())
	if !v.loadingMore {
		v.viewport.GotoBottom()
	}
	v.lastRenderedWidth = v.width
}

// sameTimeGroup returns true when b is within 5 minutes after a (both non-zero).
func sameTimeGroup(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	delta := b.Sub(a)
	return delta >= 0 && delta <= 5*time.Minute
}

// sameDay returns true when two timestamps fall on the same local calendar day.
func sameDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return a.IsZero() && b.IsZero()
	}
	ya, ma, da := a.Local().Date()
	yb, mb, db := b.Local().Date()
	return ya == yb && ma == mb && da == db
}

// formatMessageDate formats a timestamp for the campfire date separator.
func formatMessageDate(t time.Time) string {
	now := time.Now()
	local := t.In(now.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case sameDay(local, now):
		return "Today"
	case sameDay(local, today.AddDate(0, 0, -1)):
		return "Yesterday"
	default:
		return local.Format("Mon, Jan 2")
	}
}

// wrapText soft-wraps text to fit within the given width.
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	var result strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			result.WriteString("\n")
		}
		result.WriteString(wrapLine(line, width))
	}
	return result.String()
}

// wrapLine wraps a single line at word boundaries using terminal display
// widths (runewidth) so wide glyphs like emoji and CJK occupy the correct
// number of terminal cells.
func wrapLine(line string, width int) string {
	if runewidth.StringWidth(line) <= width {
		return line
	}
	var result strings.Builder
	col := 0
	words := strings.Fields(line)
	for i, word := range words {
		wlen := runewidth.StringWidth(word)
		if i > 0 && col+1+wlen > width {
			result.WriteString("\n")
			col = 0
		} else if i > 0 {
			result.WriteString(" ")
			col++
		}
		// Handle words wider than the available width
		if wlen > width && col == 0 {
			runes := []rune(word)
			lineWidth := 0
			for j, r := range runes {
				rw := runewidth.RuneWidth(r)
				if lineWidth+rw > width && lineWidth > 0 {
					result.WriteString("\n")
					lineWidth = 0
				}
				if j == 0 || lineWidth > 0 || rw > 0 {
					result.WriteRune(r)
					lineWidth += rw
				}
			}
			col = lineWidth
		} else {
			result.WriteString(word)
			col += wlen
		}
	}
	return result.String()
}

func (v *Campfire) resizeViewport() {
	// Reserve lines for the composer input area
	inputHeight := 2
	if v.composer.Mode() == widget.ComposerRich {
		inputHeight = 5
	}
	if len(v.composer.Attachments()) > 0 {
		inputHeight++
	}

	vpHeight := v.height - inputHeight
	if vpHeight < 1 {
		vpHeight = 1
	}
	v.viewport.Width = v.width
	v.viewport.Height = vpHeight
	v.composer.SetSize(v.width, inputHeight)
}

// View implements tea.Model.
func (v *Campfire) View() string {
	if v.width == 0 || v.height == 0 {
		return ""
	}

	if v.loading && len(v.lines) == 0 {
		return lipgloss.NewStyle().
			Width(v.width).
			Height(v.height).
			Padding(1, 2).
			Render(v.spinner.View() + " Loading campfire…")
	}

	theme := v.styles.Theme()

	// Message stream
	stream := v.viewport.View()

	// Mode indicator
	var modeIndicator string
	if v.mode == campfireModeScroll {
		modeIndicator = lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("scroll mode ")
	}

	separator := lipgloss.NewStyle().
		Width(v.width).
		Foreground(theme.Border).
		Render(strings.Repeat("─", v.width))

	inputLine := modeIndicator + v.composer.View()

	return lipgloss.JoinVertical(lipgloss.Left,
		stream,
		separator,
		inputLine,
	)
}

// FocusedItem implements workspace.FocusedRecording.
func (v *Campfire) FocusedItem() workspace.FocusedItemScope {
	return workspace.FocusedItemScope{} // no single-item URL for chat stream
}

func (v *Campfire) updateLastID() {
	for _, line := range v.lines {
		if line.ID > v.lastID {
			v.lastID = line.ID
		}
	}
}

func (v *Campfire) schedulePoll() tea.Cmd {
	interval := v.pool.PollInterval()
	if interval == 0 {
		return nil
	}
	key := v.pool.Key()
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: key}
	})
}

// -- Commands

func (v *Campfire) fetchOlderLines() tea.Cmd {
	projectID := v.projectID
	campfireID := v.campfireID
	page := v.currentPage
	ctx := v.session.Hub().ProjectContext()
	client := v.session.AccountClient()
	return func() tea.Msg {
		path := fmt.Sprintf("/buckets/%d/chats/%d/lines.json?page=%d", projectID, campfireID, page)
		resp, err := client.Get(ctx, path)
		if err != nil {
			return workspace.CampfireLinesLoadedMsg{Err: err, Prepend: true}
		}

		var lines []campfireLineJSON
		if err := json.Unmarshal(resp.Data, &lines); err != nil {
			return workspace.CampfireLinesLoadedMsg{Err: err, Prepend: true}
		}

		totalCount := parseTotalCountHeader(resp.Headers)

		infos := make([]workspace.CampfireLineInfo, 0, len(lines))
		for _, line := range lines {
			creator := ""
			if line.Creator != nil {
				creator = line.Creator.Name
			}
			infos = append(infos, workspace.CampfireLineInfo{
				ID:          line.ID,
				Body:        line.Content,
				Creator:     creator,
				CreatedAt:   line.CreatedAt.Format("3:04pm"),
				CreatedAtTS: line.CreatedAt,
			})
		}

		// API returns newest-first; reverse for chronological display
		reverseLines(infos)

		return workspace.CampfireLinesLoadedMsg{
			Lines:      infos,
			TotalCount: totalCount,
			Prepend:    true,
		}
	}
}

// campfireLineJSON mirrors the API JSON shape for manual parsing.
type campfireLineJSON struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Creator   *struct {
		Name string `json:"name"`
	} `json:"creator"`
}

func parseTotalCountHeader(headers http.Header) int {
	val := headers.Get("X-Total-Count")
	if val == "" {
		// Try the Basecamp-specific header format
		link := headers.Get("Link")
		if link != "" {
			// Extract total from Link header if available
			re := regexp.MustCompile(`page=(\d+)[^;]*;\s*rel="last"`)
			if m := re.FindStringSubmatch(link); len(m) >= 2 {
				if n, err := strconv.Atoi(m[1]); err == nil {
					return n * 15 // approximate
				}
			}
		}
		return 0
	}
	n, _ := strconv.Atoi(val)
	return n
}

func reverseLines(lines []workspace.CampfireLineInfo) {
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
}
