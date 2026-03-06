package views

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/summarize"
)

// volumeIntervals maps volume level to poll interval.
// 0=live(5s), 1=active(15s), 2=normal(30s), 3=slow(60s), 4=off
var volumeIntervals = [5]time.Duration{
	5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second, 0,
}

var volumeLabels = [5]string{"5s", "15s", "30s", "60s", "off"}

// riverMode tracks user interaction state.
type riverMode int

const (
	riverModeScroll riverMode = iota
	riverModeInput
)

// riverKeyMap defines river-specific keybindings.
type riverKeyMap struct {
	EnterInput   key.Binding
	ScrollMode   key.Binding
	ScrollUp     key.Binding
	ScrollDown   key.Binding
	ScrollTop    key.Binding
	ScrollBottom key.Binding
	CycleRoom    key.Binding
	Briefing     key.Binding
	Mixer        key.Binding
	ExpandGap    key.Binding
}

func defaultRiverKeyMap() riverKeyMap {
	return riverKeyMap{
		EnterInput:   key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "input")),
		ScrollMode:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "scroll")),
		ScrollUp:     key.NewBinding(key.WithKeys("k", "up")),
		ScrollDown:   key.NewBinding(key.WithKeys("j", "down")),
		ScrollTop:    key.NewBinding(key.WithKeys("g")),
		ScrollBottom: key.NewBinding(key.WithKeys("G")),
		CycleRoom:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "cycle room")),
		Briefing:     key.NewBinding(key.WithKeys("B"), key.WithHelp("B", "briefing")),
		Mixer:        key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "mixer")),
		ExpandGap:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "expand/collapse")),
	}
}

// idleTracker tracks user activity for briefing mode.
type idleTracker struct {
	lastActivity  time.Time
	wasIdle       bool
	idleThreshold time.Duration
}

func newIdleTracker() *idleTracker {
	return &idleTracker{
		lastActivity:  time.Now(),
		idleThreshold: 5 * time.Minute,
	}
}

func (it *idleTracker) RecordActivity() {
	it.lastActivity = time.Now()
	it.wasIdle = false
}

func (it *idleTracker) RecordTerminalFocus() bool {
	wasIdle := time.Since(it.lastActivity) > it.idleThreshold
	if wasIdle {
		it.wasIdle = true
	}
	it.lastActivity = time.Now()
	return wasIdle
}

// watermark records when the user was away from a room.
type watermark struct {
	awayStart time.Time
	awayEnd   time.Time
	lineIDAt  int64 // last-read line ID at time of idle
}

// River is the multi-campfire river view.
type River struct {
	session     *workspace.Session
	styles      *tui.Styles
	segmenter   *data.Segmenter
	readTracker *data.ReadTracker
	keys        riverKeyMap

	rooms    []data.BonfireRoomConfig
	roomPool *data.Pool[[]data.BonfireRoomConfig]

	linePools map[string]*data.Pool[data.CampfireLinesResult]
	pollGens  map[string]uint64

	viewport viewport.Model
	composer textinput.Model

	width, height int
	mode          riverMode
	activeRoom    int // index into rooms for Tab cycling

	// Briefing state
	briefingMode bool
	idleTracker  *idleTracker
	watermarks   map[string]watermark // per-room watermark state
	expandedGaps map[string]bool      // expanded gap regions

	// Mixer state
	mixerActive bool
	mixerStore  *data.MixerStore
	mixerCursor int
	volumes     map[string]int // room key -> volume level (0-4)

	spinner spinner.Model
	loading bool
}

// NewRiver creates a new River view.
func NewRiver(session *workspace.Session) *River {
	styles := session.Styles()

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	ti.CharLimit = 10000

	var readTracker *data.ReadTracker
	var mixerStore *data.MixerStore
	if app := session.App(); app != nil && app.Config.CacheDir != "" {
		readTracker = data.NewReadTracker(app.Config.CacheDir)
		readTracker.LoadFromDisk()
		mixerStore = data.NewMixerStore(app.Config.CacheDir)
	}

	r := &River{
		session:      session,
		styles:       styles,
		segmenter:    data.NewSegmenter(data.DefaultSegmenterConfig()),
		readTracker:  readTracker,
		mixerStore:   mixerStore,
		keys:         defaultRiverKeyMap(),
		volumes:      make(map[string]int),
		linePools:    make(map[string]*data.Pool[data.CampfireLinesResult]),
		pollGens:     make(map[string]uint64),
		viewport:     viewport.New(),
		composer:     ti,
		spinner:      s,
		loading:      true,
		idleTracker:  newIdleTracker(),
		watermarks:   make(map[string]watermark),
		expandedGaps: make(map[string]bool),
	}

	if r.mixerStore != nil {
		if saved, err := r.mixerStore.Load(); err == nil {
			r.volumes = saved.Volumes
		}
	}

	return r
}

func (r *River) Init() tea.Cmd {
	hub := r.session.Hub()
	r.roomPool = hub.BonfireRooms()
	ctx := r.session.Context()
	return tea.Batch(
		r.spinner.Tick,
		r.roomPool.Fetch(ctx),
	)
}

func (r *River) Update(msg tea.Msg) (workspace.View, tea.Cmd) {
	switch msg := msg.(type) {
	case data.PoolUpdatedMsg:
		return r, r.handlePoolUpdate(msg)

	case data.PollMsg:
		return r, r.handlePoll(msg)

	case summarize.SummaryReadyMsg:
		r.renderSegments()
		return r, nil

	case workspace.CampfireLineSentMsg:
		if msg.Err != nil {
			return r, workspace.ReportError(msg.Err, "sending message")
		}
		return r, nil

	case workspace.RefreshMsg:
		r.roomPool.Invalidate()
		for _, pool := range r.linePools {
			pool.Invalidate()
		}
		r.loading = true
		return r, tea.Batch(r.spinner.Tick, r.roomPool.Fetch(r.session.Context()))

	case workspace.FocusMsg:
		for _, pool := range r.linePools {
			pool.SetFocused(true)
		}
		if r.mode == riverModeInput {
			return r, r.composer.Focus()
		}
		return r, nil

	case workspace.BlurMsg:
		for _, pool := range r.linePools {
			pool.SetFocused(false)
		}
		r.composer.Blur()
		if r.readTracker != nil {
			_ = r.readTracker.Flush()
		}

	case workspace.TerminalFocusMsg:
		cmds := make([]tea.Cmd, 0, len(r.rooms)+1)
		// Capture awayStart before RecordTerminalFocus updates lastActivity.
		awayStart := r.idleTracker.lastActivity
		if r.idleTracker.RecordTerminalFocus() {
			awayEnd := time.Now()
			for _, room := range r.rooms {
				rkey := room.Key()
				pool, ok := r.linePools[rkey]
				if !ok {
					continue
				}
				snap := pool.Get()
				if !snap.HasData || len(snap.Data.Lines) == 0 {
					continue
				}
				lastLine := snap.Data.Lines[len(snap.Data.Lines)-1]
				r.watermarks[rkey] = watermark{
					awayStart: awayStart,
					awayEnd:   awayEnd,
					lineIDAt:  lastLine.ID,
				}
			}
		}
		cmds = append(cmds, r.rescheduleAllPolls())
		return r, tea.Batch(cmds...)

	case spinner.TickMsg:
		if r.loading {
			var cmd tea.Cmd
			r.spinner, cmd = r.spinner.Update(msg)
			return r, cmd
		}

	case tea.KeyPressMsg:
		return r, r.handleKey(msg)
	}

	return r, nil
}

func (r *River) handlePoolUpdate(msg data.PoolUpdatedMsg) tea.Cmd {
	if msg.Key == r.roomPool.Key() {
		return r.onRoomsUpdated()
	}

	// Check if it's a line pool update
	for _, room := range r.rooms {
		poolKey := fmt.Sprintf("bonfire-lines:%s", room.Key())
		if msg.Key == poolKey {
			return r.onLinesUpdated(room)
		}
	}
	return nil
}

func (r *River) onRoomsUpdated() tea.Cmd {
	snap := r.roomPool.Get()
	if !snap.HasData {
		return nil
	}

	// Cap at 8 rooms, distributed across accounts so every account
	// gets representation before any account gets a second room.
	rooms := data.CapRoomsRoundRobin(snap.Data, 8)

	// Build set of current room keys for teardown check.
	currentKeys := make(map[string]struct{}, len(rooms))
	for _, room := range rooms {
		currentKeys[room.Key()] = struct{}{}
	}

	// Tear down pools/state for rooms that fell out of the set.
	for rkey, pool := range r.linePools {
		if _, ok := currentKeys[rkey]; !ok {
			r.pollGens[rkey]++ // invalidate any in-flight timer
			pool.Clear()
			delete(r.linePools, rkey)
			delete(r.pollGens, rkey)
			delete(r.volumes, rkey)
			delete(r.watermarks, rkey)
			// Clean up expanded gap keys for this room.
			for gk := range r.expandedGaps {
				if strings.HasPrefix(gk, rkey+":") {
					delete(r.expandedGaps, gk)
				}
			}
			r.segmenter.PruneRoom(rkey)
		}
	}

	// Preserve focused room by identity, not index.
	var prevActiveKey string
	if r.activeRoom < len(r.rooms) {
		prevActiveKey = r.rooms[r.activeRoom].Key()
	}
	var prevMixerKey string
	if r.mixerCursor < len(r.rooms) {
		prevMixerKey = r.rooms[r.mixerCursor].Key()
	}

	r.rooms = rooms
	r.loading = false

	// Restore cursors by room key; fall back to clamping if room disappeared.
	r.activeRoom = indexOfRoomKey(r.rooms, prevActiveKey)
	r.mixerCursor = indexOfRoomKey(r.rooms, prevMixerKey)

	// Create line pools for new rooms
	var cmds []tea.Cmd
	ctx := r.session.Context()
	hub := r.session.Hub()
	for _, room := range rooms {
		roomKey := room.Key()
		if _, exists := r.linePools[roomKey]; !exists {
			pool := hub.BonfireLines(room.RoomID)
			r.linePools[roomKey] = pool
			cmds = append(cmds, pool.Fetch(ctx))
			cmds = append(cmds, r.scheduleRoomPoll(roomKey))
		}
	}

	r.updateComposerPrompt()

	// Set focused room to faster poll interval
	if cmd := r.updatePollFocus(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (r *River) onLinesUpdated(room data.BonfireRoomConfig) tea.Cmd {
	roomKey := room.Key()
	pool, ok := r.linePools[roomKey]
	if !ok {
		return nil
	}

	snap := pool.Get()
	if !snap.HasData {
		return nil
	}

	// Ingest into segmenter
	r.segmenter.IngestSnapshot(room.RoomID, room.ProjectName, snap.Data.Lines)
	r.segmenter.SealStale(time.Now(), 10*time.Minute)

	// Mark read
	if r.readTracker != nil && len(snap.Data.Lines) > 0 {
		lastLine := snap.Data.Lines[len(snap.Data.Lines)-1]
		r.readTracker.MarkRead(room.RoomID, lastLine.ID)
	}

	r.renderSegments()

	// Kick off async LLM summaries for gap regions
	if r.briefingMode && r.session.Summarizer() != nil {
		var cmds []tea.Cmd
		for _, seg := range r.segmenter.Segments() {
			if r.isInGapRegion(seg) && len(seg.Lines) > 0 {
				sumSegs := r.buildGapSummarySegments(seg)
				cmd := r.session.Summarizer().Summarize(summarize.Request{
					ContentKey:  r.gapContentKey(seg),
					Content:     sumSegs,
					TargetChars: 200,
					Hint:        summarize.HintGap,
				})
				if cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
		}
		if len(cmds) > 0 {
			return tea.Batch(cmds...)
		}
	}

	return nil
}

func (r *River) handlePoll(msg data.PollMsg) tea.Cmd {
	gen, ok := r.pollGens[msg.Tag]
	if !ok || msg.Gen != gen {
		return nil
	}

	pool, ok := r.linePools[msg.Tag]
	if !ok {
		return nil
	}
	return tea.Batch(
		pool.FetchIfStale(r.session.Context()),
		r.scheduleRoomPoll(msg.Tag),
	)
}

func (r *River) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if r.mode == riverModeInput {
		return r.handleInputKey(msg)
	}
	return r.handleScrollKey(msg)
}

func (r *River) handleScrollKey(msg tea.KeyPressMsg) tea.Cmd {
	r.idleTracker.RecordActivity()

	if r.mixerActive {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("h", "left"))):
			if r.mixerCursor > 0 {
				r.mixerCursor--
			}
			return nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("l", "right"))):
			if r.mixerCursor < len(r.rooms)-1 {
				r.mixerCursor++
			}
			return nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("k", "up"))):
			return r.adjustVolume(r.mixerCursor, -1)
		case key.Matches(msg, key.NewBinding(key.WithKeys("j", "down"))):
			return r.adjustVolume(r.mixerCursor, 1)
		case key.Matches(msg, key.NewBinding(key.WithKeys("s"))):
			return r.soloRoom(r.mixerCursor)
		case key.Matches(msg, key.NewBinding(key.WithKeys("m"))):
			return r.muteRoom(r.mixerCursor)
		case key.Matches(msg, r.keys.Mixer):
			r.mixerActive = false
			return nil
		case key.Matches(msg, r.keys.ScrollMode):
			r.mixerActive = false
			return nil
		}
		return nil
	}

	switch {
	case key.Matches(msg, r.keys.ExpandGap):
		if r.briefingMode {
			if len(r.expandedGaps) > 0 {
				r.expandedGaps = make(map[string]bool)
			} else {
				for _, seg := range r.segmenter.Segments() {
					if r.isInGapRegion(seg) {
						gk := r.gapKey(seg)
						if gk != "" {
							r.expandedGaps[gk] = true
						}
					}
				}
			}
			r.renderSegments()
		}
		return nil

	case key.Matches(msg, r.keys.EnterInput):
		r.mode = riverModeInput
		return r.composer.Focus()

	case key.Matches(msg, r.keys.ScrollUp):
		r.viewport.ScrollUp(1)
	case key.Matches(msg, r.keys.ScrollDown):
		r.viewport.ScrollDown(1)
	case key.Matches(msg, r.keys.ScrollTop):
		r.viewport.GotoTop()
	case key.Matches(msg, r.keys.ScrollBottom):
		r.viewport.GotoBottom()

	case key.Matches(msg, r.keys.CycleRoom):
		if len(r.rooms) > 0 {
			r.activeRoom = (r.activeRoom + 1) % len(r.rooms)
			r.updateComposerPrompt()
			return r.updatePollFocus()
		}

	case key.Matches(msg, r.keys.Briefing):
		r.briefingMode = !r.briefingMode
		r.renderSegments()
		if r.briefingMode {
			return r.kickOffGapSummaries()
		}

	case key.Matches(msg, r.keys.Mixer):
		r.mixerActive = !r.mixerActive
	}
	return nil
}

func (r *River) handleInputKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, r.keys.ScrollMode):
		r.mode = riverModeScroll
		r.composer.Blur()
		return nil

	case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
		content := r.composer.Value()
		if strings.TrimSpace(content) == "" {
			return nil
		}
		r.composer.SetValue("")
		return r.sendLine(content)
	}

	// Forward to composer
	var cmd tea.Cmd
	r.composer, cmd = r.composer.Update(msg)
	return cmd
}

func (r *River) sendLine(content string) tea.Cmd {
	if len(r.rooms) == 0 || r.activeRoom >= len(r.rooms) {
		return nil
	}
	room := r.rooms[r.activeRoom]
	ctx := r.session.Context()
	client := r.session.MultiStore().ClientFor(room.AccountID)
	if client == nil {
		return workspace.ReportError(fmt.Errorf("no client for account %s", room.AccountID), "sending message")
	}
	campfireID := room.CampfireID
	return func() tea.Msg {
		_, err := client.Campfires().CreateLine(ctx, campfireID, content, (*basecamp.CreateLineOptions)(nil))
		if err != nil {
			return workspace.CampfireLineSentMsg{Err: err}
		}
		return workspace.CampfireLineSentMsg{}
	}
}

func (r *River) isInGapRegion(seg *data.Segment) bool {
	wm, ok := r.watermarks[seg.RoomID.Key()]
	if !ok {
		return false
	}
	return seg.Sealed && seg.EndTime.After(wm.awayStart.Add(-time.Minute)) && seg.StartTime.Before(wm.awayEnd)
}

func (r *River) gapKey(seg *data.Segment) string {
	if len(seg.Lines) == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d-%d", seg.RoomID.Key(), seg.Lines[0].ID, seg.Lines[len(seg.Lines)-1].ID)
}

func (r *River) buildGapSummarySegments(seg *data.Segment) []summarize.Segment {
	sumSegs := make([]summarize.Segment, 0, len(seg.Lines))
	for _, line := range seg.Lines {
		sumSegs = append(sumSegs, summarize.Segment{
			Author: line.Creator,
			Time:   line.CreatedAt,
			Text:   data.StripTags(line.Body),
		})
	}
	return sumSegs
}

// kickOffGapSummaries fires async LLM summarize calls for all existing gap regions.
// Called when briefing mode is toggled on after data is already loaded.
func (r *River) kickOffGapSummaries() tea.Cmd {
	if r.session.Summarizer() == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, seg := range r.segmenter.Segments() {
		if r.isInGapRegion(seg) && len(seg.Lines) > 0 {
			cmd := r.session.Summarizer().Summarize(summarize.Request{
				ContentKey:  r.gapContentKey(seg),
				Content:     r.buildGapSummarySegments(seg),
				TargetChars: 200,
				Hint:        summarize.HintGap,
			})
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (r *River) gapContentKey(seg *data.Segment) string {
	return fmt.Sprintf("campfire:%d:gap:%d-%d", seg.RoomID.CampfireID, seg.Lines[0].ID, seg.Lines[len(seg.Lines)-1].ID)
}

func (r *River) renderWatermark(b *strings.Builder, theme tui.Theme) {
	// Pick the earliest watermark across all rooms for deterministic placement.
	var earliest *watermark
	for _, wm := range r.watermarks {
		wm := wm
		if earliest == nil || wm.awayStart.Before(earliest.awayStart) {
			earliest = &wm
		}
	}
	if earliest == nil {
		return
	}
	awayDuration := earliest.awayEnd.Sub(earliest.awayStart)
	var durationStr string
	if awayDuration < time.Hour {
		durationStr = fmt.Sprintf("%d min", int(awayDuration.Minutes()))
	} else {
		durationStr = fmt.Sprintf("%.1f hr", awayDuration.Hours())
	}
	line := fmt.Sprintf("\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550 You were away (%s) \u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550", durationStr)
	b.WriteString(lipgloss.NewStyle().Foreground(theme.Warning).Render(line))
	b.WriteString("\n")
}

func (r *River) renderSegments() {
	segments := r.segmenter.Segments()
	if len(segments) == 0 {
		r.viewport.SetContent("No messages yet. Waiting for campfire activity...")
		return
	}

	theme := r.styles.Theme()
	var b strings.Builder

	// Find the watermark boundary for rendering
	var watermarkRendered bool

	for i, seg := range segments {
		if i > 0 {
			b.WriteString("\n")
		}

		// Render watermark line before the first post-watermark segment.
		// Use earliest watermark for deterministic placement.
		if r.briefingMode && !watermarkRendered && len(r.watermarks) > 0 {
			var earliestWM *watermark
			for _, wm := range r.watermarks {
				wm := wm
				if earliestWM == nil || wm.awayStart.Before(earliestWM.awayStart) {
					earliestWM = &wm
				}
			}
			if earliestWM != nil && seg.StartTime.After(earliestWM.awayStart) {
				r.renderWatermark(&b, theme)
				watermarkRendered = true
			}
		}

		// Room color from deterministic hash
		colorIdx := seg.RoomID.Color(len(theme.RoomColors))
		var roomColor lipgloss.Style
		if colorIdx < len(theme.RoomColors) {
			roomColor = lipgloss.NewStyle().Foreground(theme.RoomColors[colorIdx])
		} else {
			roomColor = lipgloss.NewStyle().Foreground(theme.Primary)
		}

		// In briefing mode, collapse gap region segments
		if r.briefingMode && r.isInGapRegion(seg) {
			gk := r.gapKey(seg)
			if gk != "" && !r.expandedGaps[gk] {
				gutter := roomColor.Render("\u258c")
				nameStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Foreground)

				summary := ""
				if r.session.Summarizer() != nil {
					sumSegs := r.buildGapSummarySegments(seg)
					result := r.session.Summarizer().SummarizeSync(summarize.Request{
						ContentKey:  r.gapContentKey(seg),
						Content:     sumSegs,
						TargetChars: 200,
						Hint:        summarize.HintGap,
					})
					summary = result.Summary
				}
				if summary == "" {
					summary = fmt.Sprintf("%d messages", len(seg.Lines))
				}

				collapsed := fmt.Sprintf("%s  %s (%d messages)  %s\n%s    %s\n",
					gutter, nameStyle.Render(seg.RoomName), len(seg.Lines), "\u25b8",
					gutter, lipgloss.NewStyle().Foreground(theme.Muted).Render(summary))
				b.WriteString(collapsed)
				continue
			}
		}

		// Segment header: room name + timestamp
		timestamp := ""
		if !seg.StartTime.IsZero() {
			timestamp = seg.StartTime.Format("3:04pm")
		}

		header := roomColor.Bold(true).Render(seg.RoomName)
		if timestamp != "" {
			header += "  " + lipgloss.NewStyle().Foreground(theme.Muted).Render(timestamp)
		}
		if seg.Sealed {
			header += lipgloss.NewStyle().Foreground(theme.Muted).Render(" (sealed)")
		}
		b.WriteString(header)
		b.WriteString("\n")

		// Lines with colored gutter
		gutter := roomColor.Render("\u258c")
		nameStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Foreground)

		for _, line := range seg.Lines {
			author := nameStyle.Render(line.Creator)
			body := data.RiverText(line.Body)

			// Truncate long messages for the river view
			if runes := []rune(body); len(runes) > 200 {
				body = string(runes[:197]) + "..."
			}

			lineText := fmt.Sprintf("%s  %s  %s", gutter, author, body)
			b.WriteString(lineText)
			b.WriteString("\n")
		}
	}

	content := b.String()
	atBottom := r.viewport.AtBottom()
	r.viewport.SetContent(content)
	if atBottom {
		r.viewport.GotoBottom()
	}
}

func (r *River) updateComposerPrompt() {
	if len(r.rooms) == 0 {
		r.composer.Prompt = "> "
		return
	}
	idx := r.activeRoom
	if idx >= len(r.rooms) {
		idx = 0
	}
	room := r.rooms[idx]
	r.composer.Prompt = fmt.Sprintf("[%s] > ", room.ProjectName)
}

func (r *River) updatePollFocus() tea.Cmd {
	var cmds []tea.Cmd
	for i, room := range r.rooms {
		roomKey := room.Key()
		pool, ok := r.linePools[roomKey]
		if !ok {
			continue
		}
		if i == r.activeRoom {
			pool.SetPollConfig(data.PoolConfig{
				FreshTTL: 2 * time.Second,
				StaleTTL: 30 * time.Second,
				PollBase: 5 * time.Second,
				PollMax:  2 * time.Minute,
			})
		} else {
			pool.SetPollConfig(data.PoolConfig{
				FreshTTL: 5 * time.Second,
				StaleTTL: 30 * time.Second,
				PollBase: 15 * time.Second,
				PollMax:  2 * time.Minute,
			})
		}
		// Bump poll gen and re-arm timer at new interval
		cmds = append(cmds, r.scheduleRoomPoll(roomKey))
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (r *River) scheduleRoomPoll(roomKey string) tea.Cmd {
	pool, ok := r.linePools[roomKey]
	if !ok {
		return nil
	}
	interval := pool.PollInterval()
	if interval == 0 {
		return nil
	}
	r.pollGens[roomKey]++
	gen := r.pollGens[roomKey]
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return data.PollMsg{Tag: roomKey, Gen: gen}
	})
}

func (r *River) rescheduleAllPolls() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(r.rooms))
	for _, room := range r.rooms {
		roomKey := room.Key()
		cmds = append(cmds, r.scheduleRoomPoll(roomKey))
	}
	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

func (r *River) adjustVolume(roomIdx, delta int) tea.Cmd {
	if roomIdx >= len(r.rooms) {
		return nil
	}
	room := r.rooms[roomIdx]
	rkey := room.Key()
	vol := r.volumes[rkey] + delta
	if vol < 0 {
		vol = 0
	}
	if vol > 4 {
		vol = 4
	}
	r.volumes[rkey] = vol
	return r.applyVolume(rkey, vol)
}

func (r *River) soloRoom(roomIdx int) tea.Cmd {
	if roomIdx >= len(r.rooms) {
		return nil
	}
	var cmds []tea.Cmd
	for i, room := range r.rooms {
		rkey := room.Key()
		if i == roomIdx {
			r.volumes[rkey] = 0 // live
		} else {
			r.volumes[rkey] = 4 // off
		}
		cmds = append(cmds, r.applyVolume(rkey, r.volumes[rkey]))
	}
	return tea.Batch(cmds...)
}

func (r *River) muteRoom(roomIdx int) tea.Cmd {
	if roomIdx >= len(r.rooms) {
		return nil
	}
	room := r.rooms[roomIdx]
	rkey := room.Key()
	if r.volumes[rkey] == 4 {
		r.volumes[rkey] = 2 // normal
	} else {
		r.volumes[rkey] = 4 // off
	}
	return r.applyVolume(rkey, r.volumes[rkey])
}

func (r *River) applyVolume(roomKey string, vol int) tea.Cmd {
	pool, ok := r.linePools[roomKey]
	if !ok {
		return nil
	}

	interval := volumeIntervals[vol]
	if interval == 0 {
		// Off: clear pool data and stop polling
		pool.Clear()
		r.pollGens[roomKey]++
		r.saveMixerVolumes()
		return nil
	}

	pool.SetPollConfig(data.PoolConfig{
		FreshTTL: interval / 3,
		StaleTTL: 30 * time.Second,
		PollBase: interval,
		PollMax:  2 * time.Minute,
	})
	r.pollGens[roomKey]++
	r.saveMixerVolumes()
	return r.scheduleRoomPoll(roomKey)
}

func (r *River) saveMixerVolumes() {
	if r.mixerStore != nil {
		_ = r.mixerStore.Save(data.MixerVolumes{Volumes: r.volumes})
	}
}

func (r *River) renderMixer() string {
	if len(r.rooms) == 0 {
		return ""
	}
	theme := r.styles.Theme()
	muted := lipgloss.NewStyle().Foreground(theme.Muted)
	var parts []string
	for i, room := range r.rooms {
		rkey := room.Key()
		vol := r.volumes[rkey]

		name := room.ProjectName

		// Volume indicator: filled/empty dots
		var dots strings.Builder
		for j := range 4 {
			if j < 4-vol {
				dots.WriteString("\u25cf")
			} else {
				dots.WriteString("\u25cb")
			}
		}

		colorIdx := room.Color(len(theme.RoomColors))
		var nameStyle lipgloss.Style
		if colorIdx < len(theme.RoomColors) {
			nameStyle = lipgloss.NewStyle().Foreground(theme.RoomColors[colorIdx])
		} else {
			nameStyle = lipgloss.NewStyle().Foreground(theme.Primary)
		}

		cell := nameStyle.Render(name) + " " + dots.String() + muted.Render(" "+volumeLabels[vol])
		if i == r.mixerCursor {
			cell = "▸ " + cell
		}
		parts = append(parts, cell)
	}
	return strings.Join(parts, "  \u2502  ")
}

func (r *River) View() string {
	if r.loading {
		return r.spinner.View() + " Loading campfire rooms..."
	}

	if len(r.rooms) == 0 {
		return r.styles.Muted.Render("No campfire rooms found. Join some projects first.")
	}

	theme := r.styles.Theme()

	// Viewport
	stream := r.viewport.View()

	// Mode indicator + separator
	var modeIndicator string
	if r.mode == riverModeScroll {
		modeIndicator = lipgloss.NewStyle().
			Foreground(theme.Muted).
			Render("scroll mode ")
	}

	separator := lipgloss.NewStyle().
		Width(r.width).
		Foreground(theme.Border).
		Render(strings.Repeat("\u2500", r.width))

	inputLine := modeIndicator + r.composer.View()

	var sections []string
	if r.mixerActive {
		sections = append(sections, r.renderMixer())
	}
	sections = append(sections, stream, separator, inputLine)

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (r *River) Title() string { return "Bonfire" }

func (r *River) ShortHelp() []key.Binding {
	if r.mode == riverModeInput {
		return []key.Binding{r.keys.ScrollMode, r.keys.CycleRoom}
	}
	return []key.Binding{r.keys.EnterInput, r.keys.CycleRoom, r.keys.Briefing, r.keys.Mixer}
}

func (r *River) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{r.keys.EnterInput, r.keys.ScrollMode},
		{r.keys.ScrollUp, r.keys.ScrollDown, r.keys.ScrollTop, r.keys.ScrollBottom},
		{r.keys.CycleRoom, r.keys.Briefing, r.keys.Mixer},
	}
}

func (r *River) SetSize(w, h int) {
	r.width = w
	r.height = h
	chromeHeight := 2 // separator + input line
	if r.mixerActive {
		chromeHeight++ // mixer row
	}
	vpHeight := h - chromeHeight
	if vpHeight < 1 {
		vpHeight = 1
	}
	r.viewport.SetWidth(w)
	r.viewport.SetHeight(vpHeight)
	r.composer.SetWidth(max(0, w-utf8.RuneCountInString(r.composer.Prompt)-1))
}

// indexOfRoomKey returns the index of the room with the given key, or 0 if not found.
func indexOfRoomKey(rooms []data.BonfireRoomConfig, key string) int {
	if key == "" {
		return 0
	}
	for i, room := range rooms {
		if room.Key() == key {
			return i
		}
	}
	return 0
}

// InputActive implements workspace.InputCapturer.
func (r *River) InputActive() bool {
	return r.mode == riverModeInput
}
