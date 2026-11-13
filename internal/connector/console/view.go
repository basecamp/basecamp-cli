package console

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// styles are the few ways the console draws text. A state is always told by
// its mark and its word as well, so nothing depends on color.
type styles struct {
	title, heading, muted, bold lipgloss.Style
	good, wait, bad, busy       lipgloss.Style
	tabOn, tabOff, selected     lipgloss.Style
}

func newStyles(theme tui.Theme) styles {
	plain := lipgloss.NewStyle()
	return styles{
		title:    plain.Bold(true).Foreground(theme.Primary),
		heading:  plain.Bold(true).Foreground(theme.Foreground),
		muted:    plain.Foreground(theme.Muted),
		bold:     plain.Bold(true),
		good:     plain.Foreground(theme.Success),
		wait:     plain.Foreground(theme.Warning),
		bad:      plain.Foreground(theme.Error),
		busy:     plain.Foreground(theme.Primary),
		tabOn:    plain.Bold(true).Foreground(theme.Primary).Underline(true),
		tabOff:   plain.Foreground(theme.Muted),
		selected: plain.Bold(true).Reverse(true),
	}
}

// tone is how a state reads: well, waiting on something, wrong, at work, or
// neither.
type tone int

const (
	toneNone tone = iota
	toneGood
	toneWait
	toneBad
	toneBusy
)

func (s styles) tone(t tone) lipgloss.Style {
	switch t {
	case toneGood:
		return s.good
	case toneWait:
		return s.wait
	case toneBad:
		return s.bad
	case toneBusy:
		return s.busy
	case toneNone:
	}
	return s.muted
}

// mark is the sign a tone is drawn with.
func (t tone) mark() string {
	switch t {
	case toneGood:
		return "✓"
	case toneWait:
		return "!"
	case toneBad:
		return "✗"
	case toneBusy:
		return "●"
	case toneNone:
	}
	return "–"
}

// plain is untrusted text made safe for one line of the terminal.
func plain(s string) string {
	return richtext.SanitizeSingleLine(s)
}

// text is untrusted text made safe for the terminal, its lines kept.
func text(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(richtext.SanitizeTerminal(s), "\t", "    ")
}

var triggerWords = map[string]string{
	"mentioned":  "Mentioned",
	"assigned":   "Assigned",
	"pinged":     "Pinged",
	"subscribed": "Comment on a thread it follows",
	"completed":  "Completed",
	"boosted":    "Boosted",
	"listened":   "Chat message",
}

var typeWords = map[string]string{
	"Comment":               "comment",
	"Todo":                  "to-do",
	"Todolist":              "to-do list",
	"Message":               "message",
	"Document":              "document",
	"Kanban::Card":          "card",
	"Kanban::Step":          "card step",
	"Chat::Lines::Text":     "chat line",
	"Chat::Lines::RichText": "chat line",
	"Upload":                "file",
	"Schedule::Entry":       "event",
	"Question::Answer":      "check-in answer",
}

var agentWords = map[string]string{"claude": "Claude Code", "codex": "Codex", "pi": "Pi"}

// specWords is "Codex · gpt-6 · high effort": the coding agent, then what
// was chosen for it.
func specWords(spec runner.Spec) string {
	if spec.Adapter == "" {
		return "not chosen"
	}
	name := agentWords[spec.Adapter]
	if name == "" {
		name = plain(spec.Adapter)
	}
	parts := []string{name}
	if spec.Model != "" {
		parts = append(parts, plain(spec.Model))
	}
	if spec.Effort != "" {
		parts = append(parts, plain(spec.Effort)+" effort")
	}
	return strings.Join(parts, " · ")
}

// workerWords is a worker's state as a person reads it.
func workerWords(state string) (string, tone) {
	switch state {
	case "running":
		return "Working", toneBusy
	case "queued":
		return "Waiting its turn", toneWait
	case "interrupted":
		return "Waiting for the connector", toneWait
	case "done":
		return "Done", toneGood
	case "failed":
		return "Failed", toneBad
	case "stopped":
		return "Stopped", toneNone
	}
	return plain(state), toneNone
}

// outcome is what became of a request the connector let in.
func outcome(r Request) (string, tone) {
	switch {
	case r.State == "discarded" && r.Reason == connector.ReasonHandedOff:
		return "Handed off", toneGood
	case r.State == "discarded" && r.Reason == connector.ReasonBeforeThisRun:
		return "Missed while stopped", toneNone
	case r.State == "discarded" && r.Reason == connector.ReasonUnreadable:
		return "Could not be read", toneBad
	case r.State == "discarded" && r.Reason == connector.ReasonByOperator:
		return "Closed without running", toneNone
	case r.State == "held":
		return "Held for review", toneWait
	case r.State == "blocked":
		if r.Reason != "" {
			return "Blocked: " + strings.ReplaceAll(plain(r.Reason), "_", " "), toneWait
		}
		return "Blocked", toneWait
	case r.State == "completed":
		return "Done", toneGood
	}
	return "Waiting", toneBusy
}

// eventWords is how a request came about: "Mentioned · comment created".
func eventWords(trigger, eventType string) string {
	what, _, _ := strings.Cut(eventType, ".")
	what = strings.ReplaceAll(plain(what), "_", " ")
	lead := triggerWords[trigger]
	if lead == "" {
		lead = plain(trigger)
	}
	if lead == "" {
		lead = "Heard"
	}
	if what == "" {
		return lead
	}
	return lead + " · " + what
}

// span is a length of time in the two units that matter: "1m 12s".
func span(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// ago is how long before now t was: "5m ago".
func ago(t, now time.Time) string {
	if t.IsZero() || t.Year() < 2000 {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours())/24)
}

// fit is s cut to width columns.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// spread is left and right on one line of width columns: right is kept
// whole and left gives way.
func spread(left, right string, width int) string {
	rightWidth := ansi.StringWidth(right)
	if rightWidth >= width {
		return fit(right, width)
	}
	left = fit(left, width-rightWidth-2)
	return left + strings.Repeat(" ", width-rightWidth-ansi.StringWidth(left)) + right
}

// wrap is s broken into lines of at most width columns, at spaces only: a
// flag such as --run or a path is never split at its hyphens, which a
// person would then copy wrong. A word longer than a line is cut.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	for paragraph := range strings.SplitSeq(s, "\n") {
		line, used := "", 0
		for _, word := range strings.Fields(paragraph) {
			wide := ansi.StringWidth(word)
			for wide > width {
				head := ansi.Truncate(word, width, "")
				if head == "" {
					// A character wider than the line: left for fit to cut.
					break
				}
				if line != "" {
					lines = append(lines, line)
					line, used = "", 0
				}
				lines = append(lines, head)
				word = ansi.TruncateLeft(word, ansi.StringWidth(head), "")
				wide = ansi.StringWidth(word)
			}
			switch {
			case line == "":
				line, used = word, wide
			case used+1+wide <= width:
				line, used = line+" "+word, used+1+wide
			default:
				lines = append(lines, line)
				line, used = word, wide
			}
		}
		lines = append(lines, line)
	}
	return lines
}

// item is one row of the activity list that can be selected: a worker, a
// request held for a person, or a request the connector heard.
type item struct {
	worker  *runner.WorkerActivity
	held    *connector.HeldStatus
	request *Request
}

// key names the row, so the selection can follow it when the list changes.
func (i item) key() string {
	switch {
	case i.worker != nil:
		return "w" + i.worker.ID
	case i.held != nil:
		return "h" + strconv.FormatInt(i.held.EventID, 10)
	}
	return "r" + strconv.FormatInt(i.request.EventID, 10)
}

// url is the recording the row is about, in Basecamp.
func (i item) url() string {
	switch {
	case i.worker != nil:
		if i.worker.Request != nil {
			return i.worker.Request.URL
		}
		return ""
	case i.held != nil:
		return i.held.RecordingURL
	}
	return i.request.URL
}

// items are the activity list's rows in the order they are drawn: what is
// worked on now, what waits for a person, what was done, what was heard.
func (m model) items() []item {
	if m.snap == nil {
		return nil
	}
	var items []item
	for i := range m.snap.Now {
		items = append(items, item{worker: &m.snap.Now[i]})
	}
	for i := range m.snap.Held {
		items = append(items, item{held: &m.snap.Held[i]})
	}
	for i := range m.snap.Done {
		items = append(items, item{worker: &m.snap.Done[i]})
	}
	for i := range m.snap.Requests {
		items = append(items, item{request: &m.snap.Requests[i]})
	}
	return items
}

// phase is the connector's state in a word or two.
func (m model) phase() (string, tone) {
	snap := m.snap
	if snap == nil {
		return "Reading", toneNone
	}
	c := snap.Connector
	switch {
	case !c.Running:
		return "Not running", toneNone
	case c.Hold != nil:
		return "On hold", toneWait
	case !c.listening():
		return "Starting", toneBusy
	case snap.Runner != nil && snap.Runner.Live:
		return "Running", toneGood
	}
	return "Listening", toneGood
}

// handsOff says the running connector hands each request to the coding
// agent session that started it, and does none of the work itself.
func (s *Snapshot) handsOff() bool {
	return s.Connector.listening() && (s.Runner == nil || !s.Runner.Live)
}

// orchestrating says what a running orchestrator is busy with, and nothing
// when it is idle. A message is a request, a worker's report or what the
// person told it. The process its session names is nearly always its turn
// in flight, but may be one a turn that ended left running, so it is said
// to be at work and not to be in a turn.
func (s *Snapshot) orchestrating() string {
	r := s.Runner
	if r == nil || !r.Live || (r.Process == 0 && r.Waiting == 0) {
		return ""
	}
	messages := strconv.Itoa(r.Waiting) + " messages"
	if r.Waiting == 1 {
		messages = "1 message"
	}
	switch {
	case r.Process == 0:
		return messages + " waiting for its next turn"
	case r.Waiting == 0:
		return "at work"
	}
	return "at work, with " + messages + " not yet handled"
}

// handsOffWords says, for a connector that hands requests off, that it
// does none of the work and how to start one that does.
func (s *Snapshot) handsOffWords() string {
	start := "--orchestrator claude"
	if s.Runner != nil && s.Runner.Orchestrator.Adapter != "" {
		start = "--run"
	}
	return "This connector was started without an orchestrator, so it does none of the work itself: it prints each request for the coding agent session that started it, and a request is worked on only if such a session is reading them. To have the connector do the work, stop it and start it with: basecamp connect -P " +
		richtext.ShellQuote(s.Profile) + " " + start
}

// listening says the running connector has started its parts: its own
// record says it runs, and is this process's.
func (c Connector) listening() bool {
	return c.Running && c.LastChange != nil && c.LastChange.State == connector.ConnectionRunning &&
		(c.PID == 0 || c.LastChange.PID == c.PID)
}

// started is when the running connector started, when it said so readably.
func (c Connector) started() time.Time {
	at, err := time.Parse(time.RFC3339, c.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return at
}

// talkLines is the conversation with the orchestrator, wrapped to width.
func (m model) talkLines(width int) []string {
	var lines []string
	say := func(who string, style lipgloss.Style, words string) {
		label := style.Render(who) + "  "
		indent := strings.Repeat(" ", ansi.StringWidth(who)+2)
		for i, line := range wrap(text(words), width-len(indent)) {
			if i == 0 {
				lines = append(lines, label+line)
			} else {
				lines = append(lines, indent+line)
			}
		}
	}
	for _, said := range m.talk {
		say("You", m.styles.bold, said.said)
		switch {
		case said.waiting:
			lines = append(lines, m.styles.muted.Render("Orchestrator  waiting for its turn, "+span(m.now().Sub(said.at))))
		case said.err != "":
			say("Not sent", m.styles.bad, said.err)
		default:
			say("Orchestrator", m.styles.busy, said.reply)
		}
	}
	return lines
}

// talkScreen is the conversation in full, on a screen of its own.
func (m model) talkScreen(width int) []string {
	if len(m.talk) > 0 {
		return append([]string{""}, m.talkLines(width)...)
	}
	words := "Nothing has been said yet. Press / to tell the orchestrator something. What you say and what it answers stay here for as long as the console is open."
	if m.snap == nil || m.snap.Runner == nil || !m.snap.Runner.Live {
		words = "Nothing has been said yet, and no orchestrator is running to talk to. What you say to one, and what it answers, stays here for as long as the console is open."
	}
	lines := []string{""}
	for _, line := range wrap(words, width) {
		lines = append(lines, m.styles.muted.Render(line))
	}
	return lines
}

// talkHeight is how many lines the conversation takes, its rule included.
func (m model) talkHeight() int {
	if len(m.talk) == 0 || m.tab == tabTalk {
		return 0
	}
	// What the screen can spare once the tab has a line of its own.
	spare := m.height - 3 - m.inputHeight() - 1 - 1
	height := min(1+min(len(m.talkLines(m.width-2)), max(3, m.height/3)), spare)
	if height < 2 {
		return 0
	}
	return height
}

// inputHeight is the line the person types on, when there is one.
func (m model) inputHeight() int {
	if m.opts.Say == nil {
		return 0
	}
	return 1
}

// bodyHeight is what is left for the tab's own lines: the screen without
// the title, the tabs, the rule under them, the conversation, the input
// and the last line.
func (m model) bodyHeight() int {
	return max(1, m.height-3-m.talkHeight()-m.inputHeight()-1)
}

func (m model) render() string {
	if m.width < 20 || m.height < 8 {
		return "The window is too small for the console."
	}
	width := m.width
	var b strings.Builder

	word, tone := m.phase()
	state := m.styles.tone(tone).Render(tone.mark() + " " + word)
	if m.snap != nil && m.snap.handsOff() {
		state += m.styles.wait.Render(" · no orchestrator")
	}
	if m.snap != nil {
		if at := m.snap.Connector.started(); m.snap.Connector.Running && !at.IsZero() {
			state += m.styles.muted.Render(" · up " + span(m.now().Sub(at)))
		}
	}
	title := m.styles.title.Render("Basecamp connector")
	if m.snap != nil {
		title += m.styles.muted.Render(" · " + plain(m.snap.Profile))
	}
	b.WriteString(spread(" "+title, state+" ", width) + "\n")

	var tabs []string
	for t := range tabCount {
		name := strconv.Itoa(int(t)+1) + " " + tabNames[t]
		if t == tabHealth {
			if worst := m.worst(); worst == toneBad || worst == toneWait {
				name += " " + worst.mark()
			}
		}
		if t == m.tab {
			tabs = append(tabs, m.styles.tabOn.Render(name))
		} else {
			tabs = append(tabs, m.styles.tabOff.Render(name))
		}
	}
	b.WriteString(fit(" "+strings.Join(tabs, "   "), width) + "\n")
	b.WriteString(m.styles.muted.Render(strings.Repeat("─", width)) + "\n")

	height := m.bodyHeight()
	lines := m.bodyLines(width - 2)
	first := min(max(0, m.scroll[m.tab]), max(0, len(lines)-height))
	for i := range height {
		if first+i < len(lines) {
			b.WriteString(" " + fit(lines[first+i], width-2))
		}
		b.WriteString("\n")
	}

	if talk := m.talkHeight(); talk > 0 {
		said := m.talkLines(width - 2)
		rule := "── Orchestrator "
		if len(said) > talk-1 {
			// Only the end fits here.
			rule += "· 4 shows all of it "
		}
		b.WriteString(m.styles.muted.Render(fit(rule+strings.Repeat("─", width), width)) + "\n")
		for _, line := range said[len(said)-(talk-1):] {
			b.WriteString(" " + fit(line, width-2) + "\n")
		}
	}
	if m.inputHeight() > 0 {
		switch {
		case m.typing:
			b.WriteString(fit(" "+m.input.View(), width) + "\n")
		case m.snap != nil && m.snap.Runner != nil && m.snap.Runner.Live:
			b.WriteString(m.styles.muted.Render(fit(" › press / to tell the orchestrator something", width)) + "\n")
		default:
			b.WriteString(m.styles.muted.Render(fit(" › no orchestrator is running to talk to", width)) + "\n")
		}
	}
	b.WriteString(fit(" "+m.lastLine(), width))
	return b.String()
}

// lastLine is the note about what the person just did, or the keys.
func (m model) lastLine() string {
	if m.note != "" {
		return m.styles.wait.Render(m.note)
	}
	var keys []string
	switch {
	case m.typing:
		keys = []string{"enter send", "esc done"}
	case m.tab == tabActivity && m.detail:
		keys = []string{"esc back", "↑↓ scroll", "o open in browser"}
	case m.tab == tabActivity:
		keys = []string{"↑↓ select", "enter details", "o open in browser"}
	default:
		keys = []string{"↑↓ scroll"}
	}
	if !m.typing {
		if m.tab == tabHealth && m.opts.Health != nil {
			keys = append(keys, "r check again")
		}
		if m.opts.Say != nil {
			keys = append(keys, "/ talk")
		}
		keys = append(keys, "tab next", "q quit")
	}
	return m.styles.muted.Render(strings.Join(keys, " · "))
}

// bodyLines are the current tab's lines, each at most width columns once
// drawn.
func (m model) bodyLines(width int) []string {
	if m.snap == nil {
		return []string{"", m.styles.muted.Render("Reading the connector's state…")}
	}
	switch m.tab {
	case tabHealth:
		return m.healthLines(width)
	case tabSettings:
		return m.settingsLines(width)
	case tabTalk:
		return m.talkScreen(width)
	case tabActivity, tabCount:
	}
	if m.detail {
		return m.detailLines(width)
	}
	lines, _ := m.activityLines(width)
	return lines
}

// section is a heading over a group of lines, with a count when there is
// one worth saying.
func (m model) section(name string, count int) string {
	if count > 0 {
		return m.styles.heading.Render(name) + m.styles.muted.Render(" "+strconv.Itoa(count))
	}
	return m.styles.heading.Render(name)
}

// activityLines are the activity list's lines, and for each of its rows
// (items) the line it is on.
func (m model) activityLines(width int) (lines []string, at []int) {
	snap, now := m.snap, m.now()
	add := func(line string) { lines = append(lines, line) }
	row := func(left, right string) {
		index := len(at)
		at = append(at, len(lines))
		if index == m.cursor {
			add(m.styles.selected.Render(spread("▸ "+ansi.Strip(left), ansi.Strip(right)+" ", width)))
			return
		}
		add(spread("  "+left, right+" ", width))
	}
	workerRow := func(w *runner.WorkerActivity) {
		word, tone := workerWords(w.State)
		left := m.styles.tone(tone).Render(tone.mark()) + " " + plain(w.ID) + "  " + workerTitle(w)
		right := specWords(runner.Spec{Adapter: w.Adapter, Model: w.Model, Effort: w.Effort}) + " · "
		switch {
		case w.State == "running" && w.StartedAt != nil:
			if doing := plain(w.Doing); doing != "" {
				left += m.styles.muted.Render("  " + doing)
			}
			right += word + " " + span(now.Sub(*w.StartedAt))
		case w.EndedAt != nil:
			right += word + " " + ago(*w.EndedAt, now)
		default:
			right += word
		}
		row(left, m.styles.muted.Render(right))
	}

	add("")
	add(m.section("Working now", len(snap.Now)))
	switch {
	case snap.handsOff():
		for _, line := range wrap(snap.handsOffWords(), width-2) {
			add("  " + m.styles.wait.Render(line))
		}
	case snap.Runner == nil:
		for _, line := range wrap("Nothing here does the work yet: no connector is running, and none has run with an orchestrator.", width-2) {
			add("  " + m.styles.muted.Render(line))
		}
	case len(snap.Now) == 0 && snap.orchestrating() == "":
		add("  " + m.styles.muted.Render("Nothing is being worked on."))
	}
	if doing := snap.orchestrating(); doing != "" {
		// The orchestrator's turn comes before any worker's: it reads what
		// came in and hands the work off. It is a line and not a row, since
		// it is nothing to open.
		left := m.styles.busy.Render(toneBusy.mark()) + " Orchestrator" + m.styles.muted.Render("  "+doing)
		add(spread("  "+left, m.styles.muted.Render(specWords(snap.Runner.Orchestrator))+" ", width))
	}
	for i := range snap.Now {
		workerRow(&snap.Now[i])
	}

	if len(snap.Held) > 0 {
		add("")
		add(m.section("Waiting for you", len(snap.Held)))
		for _, h := range snap.Held {
			left := m.styles.wait.Render(toneWait.mark()) + " " + eventWords(h.Trigger, h.EventType) + m.styles.muted.Render(" · held for review")
			row(left, m.styles.muted.Render(ago(h.UpdatedAt, now)))
		}
	}

	if len(snap.Done) > 0 {
		add("")
		add(m.section("Done", 0))
		for i := range snap.Done {
			workerRow(&snap.Done[i])
		}
	}

	add("")
	add(m.section("Heard", 0))
	if len(snap.Requests) == 0 {
		add("  " + m.styles.muted.Render("Nothing has been heard yet."))
	}
	for _, r := range snap.Requests {
		word, tone := outcome(r)
		left := m.styles.tone(tone).Render(tone.mark()) + " " + eventWords(r.Trigger, r.EventType)
		if project := requestProject(r); project != "" {
			left += m.styles.muted.Render(" · " + project)
		}
		row(left, m.styles.muted.Render(word+" · "+ago(r.At, now)))
	}
	return lines, at
}

// workerTitle is what a worker was asked on: the recording's title and the
// project it is in, or the event when the request is no longer kept.
func workerTitle(w *runner.WorkerActivity) string {
	if w.Request == nil {
		if w.EventID != 0 {
			return "Event " + strconv.FormatInt(w.EventID, 10)
		}
		return "A request"
	}
	title := plain(w.Request.Title)
	if title == "" {
		title = "A " + recordingWords(w.Request.Type)
	}
	switch {
	case w.Request.Ping:
		title += " · Ping"
	case w.Request.ProjectName != "":
		title += " · " + plain(w.Request.ProjectName)
	}
	return title
}

// recordingWords is a recording's type as a person says it.
func recordingWords(kind string) string {
	if word := typeWords[kind]; word != "" {
		return word
	}
	return "request"
}

// requestProject is where a request was made.
func requestProject(r Request) string {
	switch {
	case r.Trigger == "pinged":
		return "Ping"
	case r.ProjectName != "":
		return plain(r.ProjectName)
	case r.ProjectID != 0:
		return "project " + strconv.FormatInt(r.ProjectID, 10)
	}
	return "Ping"
}

// capWords is a cap on workers running at once as a person reads it.
func capWords(limit int) string {
	switch limit {
	case 0:
		return "no limit: every request gets a worker as it arrives"
	case 1:
		return "one worker at a time: a request past that waits its turn"
	}
	return "at most " + strconv.Itoa(limit) + " workers: a request past that waits its turn"
}

// fact is one "Name  value" line of a details view, value wrapped.
func (m model) fact(lines *[]string, name, value string, width int) {
	if value == "" {
		return
	}
	const label = 14
	for i, line := range wrap(value, width-label-2) {
		head := strings.Repeat(" ", label)
		if i == 0 {
			head = m.styles.muted.Render(fmt.Sprintf("%-*s", label, name))
		}
		*lines = append(*lines, "  "+head+line)
	}
}

// detailLines are the selected row in full.
func (m model) detailLines(width int) []string {
	items := m.items()
	if m.cursor >= len(items) {
		return nil
	}
	it, now := items[m.cursor], m.now()
	lines := []string{""}
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("Jan 2 15:04:05") + " (" + ago(t, now) + ")"
	}
	switch {
	case it.worker != nil:
		w := it.worker
		word, tone := workerWords(w.State)
		lines = append(lines, m.styles.heading.Render(fit(workerTitle(w), width)), "")
		m.fact(&lines, "Worker", plain(w.ID)+" · "+m.styles.tone(tone).Render(tone.mark()+" "+word), width)
		m.fact(&lines, "Runs on", specWords(runner.Spec{Adapter: w.Adapter, Model: w.Model, Effort: w.Effort}), width)
		m.fact(&lines, "Chosen by", plain(w.Matched), width)
		m.fact(&lines, "Repo", plain(w.Repo), width)
		if r := w.Request; r != nil {
			asked := triggerWords[r.Trigger]
			if asked == "" {
				asked = "Asked"
			}
			if r.Requester != "" {
				asked += " by " + plain(r.Requester)
			}
			m.fact(&lines, "Request", asked+" on a "+recordingWords(r.Type), width)
			m.fact(&lines, "Address", plain(r.URL), width)
		}
		if w.Requests > 1 {
			m.fact(&lines, "Requests", strconv.Itoa(w.Requests)+" on this thread", width)
		}
		if w.StartedAt != nil {
			m.fact(&lines, "Started", stamp(*w.StartedAt), width)
		}
		if w.EndedAt != nil {
			m.fact(&lines, "Ended", stamp(*w.EndedAt), width)
		}
		m.fact(&lines, "Doing", plain(w.Doing), width)
		if w.Refused > 0 {
			m.fact(&lines, "Refused", strconv.Itoa(w.Refused)+" tool calls its coding agent would not make", width)
		}
		if w.CostUSD > 0 {
			m.fact(&lines, "Cost", fmt.Sprintf("$%.2f", w.CostUSD), width)
		}
		if w.Process != 0 && w.State != "running" {
			m.fact(&lines, "Left running", fmt.Sprintf("process group %d: its session gets no further turn until that has ended", w.Process), width)
		}
		if w.Error != "" {
			lines = append(lines, "", m.styles.bad.Render("Error"))
			for _, line := range wrap(text(w.Error), width-2) {
				lines = append(lines, "  "+line)
			}
		}
		if w.Report != "" {
			lines = append(lines, "", m.styles.heading.Render("Report"))
			for _, line := range wrap(text(w.Report), width-2) {
				lines = append(lines, "  "+line)
			}
		}
	case it.held != nil:
		h := it.held
		lines = append(lines, m.styles.heading.Render(eventWords(h.Trigger, h.EventType)+" · held for review"), "")
		m.fact(&lines, "Event", strconv.FormatInt(h.EventID, 10), width)
		m.fact(&lines, "Project", strconv.FormatInt(h.BucketID, 10), width)
		m.fact(&lines, "Why", strings.ReplaceAll(plain(h.Reason), "_", " "), width)
		m.fact(&lines, "Since", stamp(h.UpdatedAt), width)
		m.fact(&lines, "Address", plain(h.RecordingURL), width)
		lines = append(lines, "", "  "+m.styles.muted.Render("Review it with basecamp connect status, then release or discard it."))
	default:
		r := it.request
		word, tone := outcome(*r)
		lines = append(lines, m.styles.heading.Render(eventWords(r.Trigger, r.EventType)), "")
		m.fact(&lines, "Outcome", m.styles.tone(tone).Render(tone.mark()+" "+word), width)
		m.fact(&lines, "Where", requestProject(*r), width)
		m.fact(&lines, "Event", strconv.FormatInt(r.EventID, 10), width)
		m.fact(&lines, "When", stamp(r.At), width)
		m.fact(&lines, "Address", plain(r.URL), width)
	}
	return lines
}

// check is one line of the health screen: a mark, a name and what is so,
// with what to do about it under it.
func (m model) check(lines *[]string, t tone, name, message, hint string, width int) {
	const label = 18
	if width-label-4 < 16 {
		// Too narrow for the name and what is so side by side: one under
		// the other.
		*lines = append(*lines, "  "+m.styles.tone(t).Render(t.mark())+" "+fit(name, width-4))
		for _, line := range wrap(message, width-4) {
			*lines = append(*lines, "    "+line)
		}
		if hint != "" {
			for _, line := range wrap(hint, width-4) {
				*lines = append(*lines, "    "+m.styles.muted.Render(line))
			}
		}
		return
	}
	for i, line := range wrap(message, width-label-4) {
		head := strings.Repeat(" ", label+2)
		if i == 0 {
			head = m.styles.tone(t).Render(t.mark()) + " " + fmt.Sprintf("%-*s", label, fit(name, label-1))
		}
		*lines = append(*lines, "  "+head+line)
	}
	if hint == "" {
		return
	}
	for _, line := range wrap(hint, width-label-4) {
		*lines = append(*lines, "  "+strings.Repeat(" ", label+2)+m.styles.muted.Render(line))
	}
}

// checkTone is a doctor status as a tone.
func checkTone(status string) tone {
	switch status {
	case StatusPass:
		return toneGood
	case StatusWarn:
		return toneWait
	case StatusFail:
		return toneBad
	}
	return toneNone
}

// worst is the most serious thing the health screen has to say, so the tab
// can say there is something to read.
func (m model) worst() tone {
	worst := toneNone
	raise := func(t tone) {
		if t == toneBad || (t == toneWait && worst != toneBad) {
			worst = t
		}
	}
	if m.health != nil {
		for _, c := range m.health.Checks {
			raise(checkTone(c.Status))
		}
		if m.snap != nil {
			for _, agent := range m.health.Agents {
				if agent.Path == "" && len(m.uses(agent.Name)) > 0 {
					raise(toneBad)
				}
			}
		}
	}
	if m.snap != nil {
		if m.snap.Connector.Hold != nil || len(m.left()) > 0 {
			raise(toneWait)
		}
		if m.snap.Runner != nil && m.snap.Runner.ConfigError != "" {
			raise(toneBad)
		}
	}
	return worst
}

// uses says what the runner's settings use a coding agent for.
func (m model) uses(adapter string) []string {
	r := m.snap.Runner
	if r == nil {
		return nil
	}
	var uses []string
	if r.Orchestrator.Adapter == adapter {
		uses = append(uses, "orchestrator")
	}
	if r.Worker.Adapter == adapter {
		uses = append(uses, "worker")
	}
	for i, rule := range r.Rules {
		if rule.Worker.Adapter == adapter && rule.Worker.Adapter != r.Worker.Adapter {
			uses = append(uses, "rule "+strconv.Itoa(i+1))
		}
	}
	return uses
}

// left are the sessions whose last turn left a process running: a worker
// that is not running, or the orchestrator's when no connector is.
func (m model) left() []string {
	var left []string
	if r := m.snap.Runner; r != nil && r.Process != 0 && !r.Live {
		left = append(left, fmt.Sprintf("The orchestrator's last turn names process group %d. The next start ends it before it continues.", r.Process))
	}
	for _, workers := range [][]runner.WorkerActivity{m.snap.Now, m.snap.Done} {
		for _, w := range workers {
			if w.Process != 0 && w.State != "running" {
				left = append(left, fmt.Sprintf("Worker %s names process group %d, which could not be confirmed ended. Its session gets no further turn until it has.", plain(w.ID), w.Process))
			}
		}
	}
	return left
}

func (m model) healthLines(width int) []string {
	snap, now := m.snap, m.now()
	c := snap.Connector
	lines := []string{"", m.styles.heading.Render("Connector")}

	switch {
	case c.Running && c.PID != 0:
		message := "Running as process " + strconv.Itoa(c.PID)
		if at := c.started(); !at.IsZero() {
			message += ", started " + ago(at, now)
		}
		m.check(&lines, toneGood, "Process", message, "", width)
	case c.Running:
		m.check(&lines, toneGood, "Process", "Running", "", width)
	default:
		message := "No connector is running for this profile."
		if c.LastChange != nil {
			message += " The last one " + lastChangeWords(c.LastChange) + " " + ago(c.LastChange.ChangedAt, now) + "."
		}
		m.check(&lines, toneNone, "Process", message, "Start it: basecamp connect -P "+richtext.ShellQuote(snap.Profile)+" --run", width)
	}

	switch {
	case !c.Running:
	case c.listening():
		m.check(&lines, toneGood, "Basecamp", "Listening for requests since "+ago(c.LastChange.ChangedAt, now), "", width)
	case c.LastChange != nil && c.LastChange.State == connector.ConnectionDisconnected && c.LastChange.PID == c.PID:
		m.check(&lines, toneBad, "Basecamp", "Disconnected "+ago(c.LastChange.ChangedAt, now)+": "+plain(c.LastChange.Detail), "", width)
	default:
		m.check(&lines, toneBusy, "Basecamp", "Starting: it checks the agent's sign-in and ends anything a crash left running before it listens.", "", width)
	}

	if r := snap.Runner; c.Running && (r != nil || snap.handsOff()) {
		switch {
		case r != nil && r.Live:
			message := "Answering on " + specWords(r.Orchestrator) + " · " + strconv.Itoa(r.Turns) + " turns"
			if r.Waiting > 0 {
				message += " · " + strconv.Itoa(r.Waiting) + " waiting"
			}
			tone := toneGood
			if r.SetAside > 0 {
				message += " · " + strconv.Itoa(r.SetAside) + " set aside after every retry failed"
				tone = toneWait
			}
			m.check(&lines, tone, "Orchestrator", message, "", width)
		case snap.handsOff():
			m.check(&lines, toneNone, "Orchestrator", "None: it was started without one, so requests are printed for the coding agent session that started it and no worker is started here.",
				"To have it do the work, stop it and start it with --run or --orchestrator.", width)
		default:
			m.check(&lines, toneBusy, "Orchestrator", "Not answering yet.", "", width)
		}
	}
	if r := snap.Runner; r != nil && r.ConfigError != "" {
		m.check(&lines, toneBad, "runner.json", "It cannot be read as written, so no agent is started: "+plain(r.ConfigError), "", width)
	}
	if c.Hold != nil {
		m.check(&lines, toneWait, "Hold", "Held since "+ago(c.Hold.HeldAt, now)+" by "+plain(c.Hold.HeldBy)+": requests are kept, and nothing is handed off or posted.",
			"Review held records in basecamp connect status, then basecamp connect release -P "+richtext.ShellQuote(snap.Profile), width)
	}
	for _, left := range m.left() {
		m.check(&lines, toneWait, "Left running", left, "", width)
	}

	lines = append(lines, "")
	switch {
	case m.opts.Health == nil:
	case m.health == nil:
		lines = append(lines, m.styles.heading.Render("Checks"), "  "+m.styles.muted.Render("Checking…"))
	default:
		head := m.styles.heading.Render("Checks")
		if m.checking {
			head += m.styles.muted.Render(" · checking again…")
		} else {
			head += m.styles.muted.Render(" · checked " + ago(m.health.At, now))
		}
		lines = append(lines, head)
		for _, check := range m.health.Checks {
			m.check(&lines, checkTone(check.Status), plain(check.Name), plain(check.Message), plain(check.Hint), width)
		}
		lines = append(lines, "", m.styles.heading.Render("Coding agents"))
		for _, agent := range m.health.Agents {
			uses := m.uses(agent.Name)
			name := agentWords[agent.Name]
			if name == "" {
				name = plain(agent.Name)
			}
			used := ""
			if len(uses) > 0 {
				used = " · used as " + strings.Join(uses, ", ")
			}
			switch {
			case agent.Path == "" && len(uses) > 0:
				m.check(&lines, toneBad, name, "Not installed, and the settings use it"+strings.TrimPrefix(used, " · used")+".", "Its command, "+plain(agent.Name)+", is not on PATH.", width)
			case agent.Path == "":
				m.check(&lines, toneNone, name, "Not installed.", "", width)
			case agent.Version == "":
				m.check(&lines, toneWait, name, "Installed, but "+plain(agent.Error)+used, plain(agent.Path), width)
			default:
				m.check(&lines, toneGood, name, plain(agent.Version)+used, plain(agent.Path), width)
			}
		}
	}

	if len(snap.Notes) > 0 {
		lines = append(lines, "", m.styles.heading.Render("Could not be read"))
		for _, note := range snap.Notes {
			m.check(&lines, toneWait, "Note", plain(note), "", width)
		}
	}
	return lines
}

// lastChangeWords is how the last connector's run ended.
func lastChangeWords(c *connector.ConnectionStatus) string {
	switch c.State {
	case connector.ConnectionStopped:
		return "stopped"
	case connector.ConnectionDisconnected:
		return "was disconnected"
	case connector.ConnectionRunning:
		return "ended without saying so, having started"
	}
	return "changed"
}

// people is a list of people by name, or by id where no name is known.
func people(list []Person) string {
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.words())
	}
	return strings.Join(names, ", ")
}

func (p Person) words() string {
	if name := plain(p.Name); name != "" {
		return name
	}
	return "person " + strconv.FormatInt(p.ID, 10)
}

func (m model) settingsLines(width int) []string {
	snap := m.snap
	lines := []string{""}
	profile := richtext.ShellQuote(snap.Profile)

	lines = append(lines, m.styles.heading.Render("Who does the work"))
	if r := snap.Runner; r == nil {
		for _, line := range wrap("The coding agent session that starts the connector, when one does. To have the connector do the work itself, start it with a coding agent: basecamp connect -P "+profile+" --orchestrator claude", width-2) {
			lines = append(lines, "  "+m.styles.muted.Render(line))
		}
	} else {
		if snap.handsOff() {
			for _, line := range wrap("The connector running now was started without an orchestrator, so it uses none of this. What follows is used when it is started to do the work itself: basecamp connect -P "+profile+" --run", width-2) {
				lines = append(lines, "  "+m.styles.muted.Render(line))
			}
			lines = append(lines, "")
		}
		m.fact(&lines, "Orchestrator", specWords(r.Orchestrator), width)
		m.fact(&lines, "Worker", specWords(r.Worker), width)
		switch r.Permissions {
		case string(runner.PermissionsGuarded):
			m.fact(&lines, "Permissions", "guarded: each coding agent runs under its own guardrails", width)
		default:
			m.fact(&lines, "Permissions", "full: each coding agent runs with its permission prompts off", width)
		}
		m.fact(&lines, "At once", capWords(r.MaxWorkers), width)
		if r.Live && r.KeptMaxWorkers != r.MaxWorkers {
			// A cap is read at a start, so an edit waits for the next one.
			m.fact(&lines, "", m.styles.wait.Render("runner.json now says "+capWords(r.KeptMaxWorkers)+", which applies when the connector next starts"), width)
		}
		if r.ConfigError != "" {
			m.fact(&lines, "runner.json", m.styles.bad.Render("cannot be read as written: "+plain(r.ConfigError)), width)
		}
		if len(r.Rules) > 0 {
			lines = append(lines, "", m.styles.heading.Render("Rules")+m.styles.muted.Render(" · the first that matches wins"))
			for i, rule := range r.Rules {
				when := make([]string, 0, len(rule.When))
				for _, condition := range rule.When {
					when = append(when, plain(condition))
				}
				for j, line := range wrap(strings.Join(when, ", and ")+" → "+specWords(rule.Worker), width-6) {
					head := "    "
					if j == 0 {
						head = fmt.Sprintf("%2d. ", i+1)
					}
					lines = append(lines, "  "+head+line)
				}
			}
		}
	}

	lines = append(lines, "", m.styles.heading.Render("Pings"))
	switch {
	case snap.Pings.Error != "":
		m.fact(&lines, "Answers", m.styles.bad.Render("cannot be told: "+plain(snap.Pings.Error)), width)
	case snap.Pings.Hears == nil:
		m.fact(&lines, "Answers", "cannot be told without the profile's credential", width)
	case *snap.Pings.Hears:
		m.fact(&lines, "Answers", "yes: "+plain(snap.Pings.Because), width)
	default:
		m.fact(&lines, "Answers", "no: "+plain(snap.Pings.Because), width)
	}
	if snap.Pings.Twice {
		m.fact(&lines, "Twice", m.styles.wait.Render("another connector on this computer answers them too"), width)
	}
	for _, guests := range snap.Pings.Guests {
		m.fact(&lines, "Ping "+strconv.FormatInt(guests.PingID, 10), "also takes work from "+people(guests.People), width)
	}

	lines = append(lines, "", m.styles.heading.Render("Who may give the agent work"))
	m.fact(&lines, "Agent", snap.Agent.words(), width)
	m.fact(&lines, "Operator", snap.Trust.Operator.words(), width)
	switch snap.Trust.Mode {
	case "allowlist":
		m.fact(&lines, "Also", people(snap.Trust.Allowlist), width)
	case "project":
		also := "everyone on a project it serves who is not a client"
		if len(snap.Trust.Allowlist) > 0 {
			also += ", and " + people(snap.Trust.Allowlist)
		}
		m.fact(&lines, "Also", also, width)
	default:
		m.fact(&lines, "Also", "nobody else", width)
	}
	if snap.Trust.AllowAssignments {
		m.fact(&lines, "Assignments", "from the people above as well as the operator", width)
	} else {
		m.fact(&lines, "Assignments", "from the operator alone", width)
	}
	if len(snap.Trust.Agents) > 0 {
		m.fact(&lines, "Other agents", people(snap.Trust.Agents)+", by mention only", width)
	}

	lines = append(lines, "", m.section("Projects it serves", len(snap.Projects)))
	if len(snap.Projects) == 0 {
		lines = append(lines, "  "+m.styles.muted.Render("None."))
	}
	for _, p := range snap.Projects {
		name := plain(p.Name)
		if name == "" {
			name = "Project " + strconv.FormatInt(p.ID, 10)
		}
		var also []string
		if p.Campfires > 0 {
			also = append(also, "answers "+strconv.Itoa(p.Campfires)+" chats without a mention")
		}
		if p.WatchCompletions {
			also = append(also, "hears to-dos being completed")
		}
		line := "  " + name
		if len(also) > 0 {
			line += m.styles.muted.Render(" · " + strings.Join(also, " · "))
		}
		lines = append(lines, fit(line, width))
	}

	lines = append(lines, "")
	for _, line := range wrap("Projects and people are changed with basecamp connect setup -P "+profile+". Who does the work is runner.json, beside connect.json: its rules and permissions apply to the next request, the rest to the next start.", width-2) {
		lines = append(lines, "  "+m.styles.muted.Render(line))
	}
	return lines
}
