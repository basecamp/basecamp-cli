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
)

// BonfireSidebar is a compact live chat/ping panel designed for the sidebar.
// Shows campfire rooms with activity and 1:1 pings, grouped and sorted by
// recency. Replaces the single-line ticker with a scannable, interactive list.
type BonfireSidebar struct {
	session *workspace.Session
	styles  *tui.Styles

	digestPool *data.Pool[[]data.BonfireDigestEntry]
	pingPool   *data.Pool[[]data.PingRoomInfo]

	digests []data.BonfireDigestEntry
	pings   []data.PingRoomInfo

	cursor  int
	offset  int // scroll offset
	focused bool

	pollGen uint64

	spinner spinner.Model
	loading bool

	width, height int
}

// NewBonfireSidebar creates a new sidebar view.
func NewBonfireSidebar(session *workspace.Session) *BonfireSidebar {
	styles := session.Styles()
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	return &BonfireSidebar{
		session: session,
		styles:  styles,
		spinner: s,
		loading: true,
	}
}

func (b *BonfireSidebar) Init() tea.Cmd {
	hub := b.session.Hub()
	b.digestPool = hub.BonfireDigest()
	b.pingPool = hub.PingRooms()
	ctx := hub.Global().Context()

	cmds := []tea.Cmd{b.spinner.Tick}

	if snap := b.digestPool.Get(); snap.Usable() {
		b.digests = snap.Data
		b.loading = false
	}
	if !b.digestPool.Get().Fresh() {
		cmds = append(cmds, b.digestPool.FetchIfStale(ctx))
	}

	if snap := b.pingPool.Get(); snap.Usable() {
		b.pings = snap.Data
	}
	if !b.pingPool.Get().Fresh() {
		cmds = append(cmds, b.pingPool.FetchIfStale(ctx))
	}

	cmds = append(cmds, b.schedulePoll())
	return tea.Batch(cmds...)
}

func (b *BonfireSidebar) Update(msg tea.Msg) (workspace.View, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		switch msg.Key {
		case b.digestPool.Key():
			if snap := b.digestPool.Get(); snap.Usable() {
				b.digests = snap.Data
				b.loading = false
			}
		case b.pingPool.Key():
			if snap := b.pingPool.Get(); snap.Usable() {
				b.pings = snap.Data
			}
		}
		b.clampCursor()
		return b, nil

	case data.PollMsg:
		if msg.Tag == "bonfire-sidebar" && msg.Gen == b.pollGen {
			ctx := b.session.Hub().Global().Context()
			return b, tea.Batch(
				b.digestPool.FetchIfStale(ctx),
				b.pingPool.FetchIfStale(ctx),
				b.schedulePoll(),
			)
		}

	case workspace.FocusMsg:
		b.focused = true
		ctx := b.session.Hub().Global().Context()
		return b, tea.Batch(
			b.digestPool.FetchIfStale(ctx),
			b.pingPool.FetchIfStale(ctx),
			b.schedulePoll(),
		)

	case workspace.BlurMsg:
		b.focused = false
		return b, nil

	case workspace.RefreshMsg:
		b.digestPool.Invalidate()
		b.pingPool.Invalidate()
		b.loading = true
		ctx := b.session.Hub().Global().Context()
		return b, tea.Batch(
			b.spinner.Tick,
			b.digestPool.Fetch(ctx),
			b.pingPool.Fetch(ctx),
		)

	case workspace.TerminalFocusMsg:
		return b, b.schedulePoll()

	case spinner.TickMsg:
		if b.loading && len(b.digests) == 0 {
			var cmd tea.Cmd
			b.spinner, cmd = b.spinner.Update(msg)
			return b, cmd
		}

	case tea.KeyPressMsg:
		if !b.focused {
			return b, nil
		}
		return b, b.handleKey(msg)
	}

	return b, nil
}

func (b *BonfireSidebar) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	total := b.totalItems()
	switch {
	case key.Matches(msg, key.NewBinding(key.WithKeys("j", "down"))):
		if b.cursor < total-1 {
			b.cursor++
			b.ensureVisible()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("k", "up"))):
		if b.cursor > 0 {
			b.cursor--
			b.ensureVisible()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("g"))):
		b.cursor = 0
		b.offset = 0
	case key.Matches(msg, key.NewBinding(key.WithKeys("G"))):
		if total > 0 {
			b.cursor = total - 1
			b.ensureVisible()
		}
	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		return b.openSelected()
	}
	return nil
}

func (b *BonfireSidebar) openSelected() tea.Cmd {
	item := b.itemAt(b.cursor)
	if item == nil {
		return nil
	}
	scope := b.session.Scope()

	switch v := item.(type) {
	case data.BonfireDigestEntry:
		scope.AccountID = v.AccountID
		scope.ProjectID = v.ProjectID
		scope.ToolType = "chat"
		scope.ToolID = v.CampfireID
		return workspace.Navigate(workspace.ViewCampfire, scope)
	case data.PingRoomInfo:
		scope.AccountID = v.AccountID
		scope.ProjectID = v.ProjectID
		scope.ToolType = "chat"
		scope.ToolID = v.CampfireID
		return workspace.Navigate(workspace.ViewCampfire, scope)
	}
	return nil
}

func (b *BonfireSidebar) View() string {
	if b.loading && len(b.digests) == 0 && len(b.pings) == 0 {
		return lipgloss.NewStyle().
			Width(b.width).
			Height(b.height).
			Padding(1, 1).
			Render(b.spinner.View() + " Loading\u2026")
	}

	if b.totalItems() == 0 {
		return lipgloss.NewStyle().
			Width(b.width).
			Height(b.height).
			Padding(1, 1).
			Foreground(b.styles.Theme().Muted).
			Render("No active chats")
	}

	theme := b.styles.Theme()
	var lines []string

	// Visible area = height lines, each item is 2 lines
	// We'll track line index vs cursor for highlighting
	itemIdx := 0

	// Campfires section
	if len(b.digests) > 0 {
		header := lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Foreground).
			Render("CAMPFIRES")
		lines = append(lines, header)

		for _, e := range b.digests {
			lines = append(lines, b.renderDigestItem(e, itemIdx == b.cursor, theme)...)
			itemIdx++
		}
	}

	// Pings section
	activePings := b.activePings()
	if len(activePings) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "") // spacer
		}
		header := lipgloss.NewStyle().
			Bold(true).
			Foreground(theme.Foreground).
			Render("PINGS")
		lines = append(lines, header)

		for _, p := range activePings {
			lines = append(lines, b.renderPingItem(p, itemIdx == b.cursor, theme)...)
			itemIdx++
		}
	}

	// Apply scrolling
	content := strings.Join(lines, "\n")
	rendered := b.applyScroll(content)

	return lipgloss.NewStyle().
		Width(b.width).
		Height(b.height).
		Render(rendered)
}

func (b *BonfireSidebar) renderDigestItem(e data.BonfireDigestEntry, selected bool, theme tui.Theme) []string {
	maxW := b.width - 2 // padding
	if maxW < 10 {
		maxW = 10
	}

	// First line: colored bullet + room name + time
	colorIdx := e.Color(len(theme.RoomColors))
	bulletColor := theme.Primary
	if colorIdx < len(theme.RoomColors) {
		bulletColor = theme.RoomColors[colorIdx]
	}

	bullet := lipgloss.NewStyle().Foreground(bulletColor).Render("\u25cf")
	name := truncateRunes(e.RoomName, maxW-8)
	elapsed := relativeTime(e.LastAtTS)

	nameStyle := lipgloss.NewStyle()
	timeStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	if selected && b.focused {
		nameStyle = nameStyle.Bold(true).Foreground(theme.Primary)
		timeStyle = timeStyle.Foreground(theme.Primary)
	}

	line1 := fmt.Sprintf(" %s %s %s",
		bullet,
		nameStyle.Render(name),
		timeStyle.Render(elapsed),
	)

	// Second line: author + message preview
	var line2 string
	if e.LastAuthor != "" || e.LastMessage != "" {
		preview := e.LastMessage
		if e.LastAuthor != "" {
			preview = e.LastAuthor + ": " + preview
		}
		preview = truncateRunes(preview, maxW-4)
		previewStyle := lipgloss.NewStyle().Foreground(theme.Secondary)
		if selected && b.focused {
			previewStyle = previewStyle.Foreground(theme.Foreground)
		}
		line2 = "   " + previewStyle.Render(preview)
	}

	if line2 != "" {
		return []string{line1, line2}
	}
	return []string{line1}
}

func (b *BonfireSidebar) renderPingItem(p data.PingRoomInfo, selected bool, theme tui.Theme) []string {
	maxW := b.width - 2
	if maxW < 10 {
		maxW = 10
	}

	bullet := lipgloss.NewStyle().Foreground(theme.Secondary).Render("\u25cb")
	name := truncateRunes(p.PersonName, maxW-8)
	elapsed := relativeTime(p.LastAtTS)

	nameStyle := lipgloss.NewStyle()
	timeStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	if selected && b.focused {
		nameStyle = nameStyle.Bold(true).Foreground(theme.Primary)
		timeStyle = timeStyle.Foreground(theme.Primary)
	}

	line1 := fmt.Sprintf(" %s %s %s",
		bullet,
		nameStyle.Render(name),
		timeStyle.Render(elapsed),
	)

	var line2 string
	if p.LastMessage != "" {
		preview := truncateRunes(p.LastMessage, maxW-4)
		previewStyle := lipgloss.NewStyle().Foreground(theme.Secondary)
		if selected && b.focused {
			previewStyle = previewStyle.Foreground(theme.Foreground)
		}
		line2 = "   " + previewStyle.Render(preview)
	}

	if line2 != "" {
		return []string{line1, line2}
	}
	return []string{line1}
}

func (b *BonfireSidebar) applyScroll(content string) string {
	lines := strings.Split(content, "\n")
	if b.height <= 0 || len(lines) <= b.height {
		return content
	}
	end := b.offset + b.height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[b.offset:end], "\n")
}

func (b *BonfireSidebar) ensureVisible() {
	// Approximate: each item is ~2 lines, headers are 1 line, spacers 1 line
	// Simpler: just track the approximate line position of the cursor
	linePos := b.estimateCursorLine()
	if linePos < b.offset {
		b.offset = linePos
	}
	if linePos+2 > b.offset+b.height {
		b.offset = linePos + 2 - b.height
		if b.offset < 0 {
			b.offset = 0
		}
	}
}

func (b *BonfireSidebar) estimateCursorLine() int {
	line := 0
	itemIdx := 0

	if len(b.digests) > 0 {
		line++ // header
		for range b.digests {
			if itemIdx == b.cursor {
				return line
			}
			line += 2 // each item ~2 lines
			itemIdx++
		}
	}

	activePings := b.activePings()
	if len(activePings) > 0 {
		if len(b.digests) > 0 {
			line++ // spacer
		}
		line++ // header
		for range activePings {
			if itemIdx == b.cursor {
				return line
			}
			line += 2
			itemIdx++
		}
	}
	return line
}

// totalItems returns the number of selectable items (digests + active pings).
func (b *BonfireSidebar) totalItems() int {
	return len(b.digests) + len(b.activePings())
}

// itemAt returns the item at the given index (digest or ping).
func (b *BonfireSidebar) itemAt(idx int) any {
	if idx < len(b.digests) {
		return b.digests[idx]
	}
	pingIdx := idx - len(b.digests)
	pings := b.activePings()
	if pingIdx < len(pings) {
		return pings[pingIdx]
	}
	return nil
}

// activePings returns pings with recent activity (within last 24h).
func (b *BonfireSidebar) activePings() []data.PingRoomInfo {
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	var active []data.PingRoomInfo
	for _, p := range b.pings {
		if p.LastAtTS > cutoff {
			active = append(active, p)
		}
	}
	return active
}

func (b *BonfireSidebar) clampCursor() {
	total := b.totalItems()
	if total == 0 {
		b.cursor = 0
		b.offset = 0
		return
	}
	if b.cursor >= total {
		b.cursor = total - 1
	}
}

func (b *BonfireSidebar) schedulePoll() tea.Cmd {
	b.pollGen++
	gen := b.pollGen
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: "bonfire-sidebar", Gen: gen}
	})
}

// relativeTime formats a unix timestamp as a compact relative time.
func relativeTime(ts int64) string {
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

// truncateRunes truncates s to maxRunes, appending an ellipsis if needed.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes < 2 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "\u2026"
}

func (b *BonfireSidebar) Title() string { return "Chats" }

func (b *BonfireSidebar) ShortHelp() []key.Binding {
	if !b.focused {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("j/k"), key.WithHelp("j/k", "navigate")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

func (b *BonfireSidebar) FullHelp() [][]key.Binding {
	return [][]key.Binding{b.ShortHelp()}
}

func (b *BonfireSidebar) SetSize(w, h int) {
	b.width = w
	b.height = h
}
