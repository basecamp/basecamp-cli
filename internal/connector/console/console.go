// Package console is the connector's console: one full-screen view, in the
// terminal, of what a profile's connector listens to, what it is working on,
// what it did, and whether it is well, with a line to talk to its
// orchestrator.
//
// # What it is beside the connector
//
// The console is a process of its own. It reads what `basecamp connect
// status` reads, the same way, and so it runs beside a connector, before
// one has started, and after one has stopped: a connector that starts while
// the console is open shows as it comes up. Closing the console leaves the
// connector as it was.
//
// # Who can open it
//
// Whoever can run the command as the person the connector runs as. It
// listens on nothing: what it shows is read from files only that person can
// read, and what it tells the orchestrator goes over the runner's socket,
// in a directory only that person can enter.
//
// # What it shows is not trusted
//
// A recording's title is whatever someone in Basecamp wrote, and a worker's
// report is whatever a coding agent wrote. Every such string is stripped of
// terminal control sequences before it is drawn (plain, text), so neither
// can move the cursor, restyle the screen or write to the terminal's title.
package console

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-cli/internal/stdinarg"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// Options are what a console reads and where it sends.
type Options struct {
	// Snapshot reads the connector's state as it stands. It is called
	// about once a second, one call at a time.
	Snapshot func(ctx context.Context) *Snapshot
	// Health runs doctor's checks and asks each coding agent its version.
	// It is called when the console opens and when the person asks again.
	Health func(ctx context.Context) *Health
	// Say gives the orchestrator a message from the person and waits for
	// its answer.
	Say func(ctx context.Context, message string) (string, error)
	// Open opens an address in the person's browser.
	Open func(url string) error
	// Theme is the palette it is drawn in.
	Theme tui.Theme
}

// snapshotEvery is how long the console waits after one snapshot before it
// reads the next.
const snapshotEvery = time.Second

// Run shows the console until the person closes it or ctx ends. It is the
// one place this package starts a bubbletea program, so it is where the
// non-interactive floor holds: without a terminal on both standard input
// and standard output it returns tui.ErrNotInteractive and draws nothing.
func Run(ctx context.Context, opts Options) error {
	if !stdinarg.InteractiveStdio() {
		return tui.ErrNotInteractive
	}
	_, err := tea.NewProgram(newModel(ctx, opts), tea.WithContext(ctx)).Run()
	// Ended from outside, by ctx or by a signal, is ended as it was asked
	// to be, and not a failure of the console.
	if ctx.Err() != nil || errors.Is(err, tea.ErrInterrupted) {
		return nil //nolint:nilerr // see above
	}
	return err
}

// tab is one of the console's screens.
type tab int

const (
	tabActivity tab = iota
	tabHealth
	tabSettings
	// tabTalk is the whole conversation with the orchestrator, of which the
	// other screens show only the end.
	tabTalk
	tabCount
)

var tabNames = [tabCount]string{"Activity", "Health", "Settings", "Talk"}

// toEnd is a scroll position past any screen's last line: the end of it,
// once drawn.
const toEnd = 1 << 30

// exchange is one thing the person told the orchestrator and its answer.
type exchange struct {
	said  string
	reply string
	err   string
	// waiting is true until the orchestrator's turn ends.
	waiting bool
	at      time.Time
}

// maxMessage bounds what one message to the orchestrator may hold.
const maxMessage = 4000

type model struct {
	ctx    context.Context //nolint:containedctx // commands run under the console's own lifetime
	opts   Options
	styles styles

	width, height int
	tab           tab

	snap *Snapshot
	// health is doctor's last answer; checking is true while it is asked.
	health   *Health
	checking bool

	// cursor is the selected row of the activity list, and selected the
	// row's own key, by which the selection follows a row that moves.
	cursor   int
	selected string
	// detail shows the selected row in full, in place of the list.
	detail bool
	// above says the person scrolled up from the first row to read what the
	// screen says over it, so the row is not brought back into view under
	// them.
	above bool
	// scroll is the first line shown of each tab's body.
	scroll [tabCount]int

	input  textinput.Model
	typing bool
	talk   []exchange
	// note is a line about the last thing the person did.
	note string
	now  func() time.Time
}

func newModel(ctx context.Context, opts Options) model {
	input := textinput.New()
	input.Prompt = "› "
	input.Placeholder = "Tell the orchestrator something"
	input.CharLimit = maxMessage
	return model{ctx: ctx, opts: opts, styles: newStyles(opts.Theme), input: input, now: time.Now, checking: opts.Health != nil}
}

type (
	snapshotMsg struct{ snap *Snapshot }
	nextMsg     struct{}
	healthMsg   struct{ health *Health }
	replyMsg    struct {
		index int
		reply string
		err   error
	}
	openedMsg struct{ err error }
)

func (m model) Init() tea.Cmd {
	return tea.Batch(m.readSnapshot(), m.readHealth())
}

func (m model) readSnapshot() tea.Cmd {
	return func() tea.Msg { return snapshotMsg{m.opts.Snapshot(m.ctx)} }
}

func (m model) readHealth() tea.Cmd {
	if m.opts.Health == nil {
		return nil
	}
	return func() tea.Msg { return healthMsg{m.opts.Health(m.ctx)} }
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(10, m.width-6))
		m.reveal()
		return m, nil
	case snapshotMsg:
		m.snap = msg.snap
		m.follow()
		return m, tea.Tick(snapshotEvery, func(time.Time) tea.Msg { return nextMsg{} })
	case nextMsg:
		return m, m.readSnapshot()
	case healthMsg:
		m.health, m.checking = msg.health, false
		return m, nil
	case replyMsg:
		if msg.index < len(m.talk) {
			said := &m.talk[msg.index]
			said.waiting, said.reply = false, msg.reply
			if msg.err != nil {
				said.err = msg.err.Error()
			}
			m.scroll[tabTalk] = toEnd
		}
		return m, nil
	case openedMsg:
		if msg.err != nil {
			m.note = "It could not be opened in a browser: " + msg.err.Error()
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.typing {
			return m.typed(msg)
		}
		return m.pressed(msg)
	}
	if m.typing {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// pressed is a key while the person is not typing a message.
func (m model) pressed(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.note = ""
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab, m.detail = (m.tab+1)%tabCount, false
	case "shift+tab", "left", "h":
		m.tab, m.detail = (m.tab+tabCount-1)%tabCount, false
	case "1", "2", "3", "4":
		m.tab, m.detail = tab(msg.String()[0]-'1'), false
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+u":
		m.move(-m.page())
	case "pgdown", "ctrl+d", "space":
		m.move(m.page())
	case "home", "g":
		m.move(-1 << 30)
	case "end", "G":
		m.move(1 << 30)
	case "enter":
		if m.tab == tabActivity && len(m.items()) > 0 {
			m.detail, m.above = !m.detail, false
			m.scroll[tabActivity] = 0
			m.reveal()
		}
	case "esc", "backspace":
		m.detail, m.above = false, false
		m.reveal()
	case "o":
		return m, m.open()
	case "r":
		if m.opts.Health != nil && !m.checking {
			m.checking = true
			return m, m.readHealth()
		}
	case "/", "i":
		if m.opts.Say == nil {
			break
		}
		if m.snap == nil || m.snap.Runner == nil || !m.snap.Runner.Live {
			m.note = "No connector is running with an orchestrator, so there is nobody to tell."
			break
		}
		m.typing = true
		return m, m.input.Focus()
	}
	return m, nil
}

// typed is a key while the person is typing a message.
func (m model) typed(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.typing = false
		m.input.Blur()
		return m, nil
	case "enter":
		message := strings.TrimSpace(m.input.Value())
		if message == "" {
			return m, nil
		}
		m.input.Reset()
		m.talk = append(m.talk, exchange{said: message, waiting: true, at: m.now()})
		m.scroll[tabTalk] = toEnd
		index, say, ctx := len(m.talk)-1, m.opts.Say, m.ctx
		return m, func() tea.Msg {
			reply, err := say(ctx, message)
			return replyMsg{index: index, reply: reply, err: err}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// move moves the selection on the activity list, and scrolls the others,
// and the activity screen too while it has no row to select.
func (m *model) move(by int) {
	items := m.items()
	if m.tab != tabActivity || m.detail || len(items) == 0 {
		last := max(0, len(m.bodyLines(m.width-2))-m.bodyHeight())
		m.scroll[m.tab] = min(max(0, min(m.scroll[m.tab], last)+clampStep(by)), last)
		return
	}
	switch {
	case by < 0 && m.cursor == 0 && m.scroll[tabActivity] > 0:
		// Up from the first row is what the screen says over its rows,
		// which a small window cannot show beside them.
		m.scroll[tabActivity] = max(0, m.scroll[tabActivity]+clampStep(by))
		m.above = true
		return
	case by > 0 && m.above:
		// And one step down from there is back to the row. A jump goes
		// where it would have from the row.
		m.above = false
		if by == 1 {
			m.reveal()
			return
		}
	}
	m.cursor = min(max(0, m.cursor+clampStep(by)), len(items)-1)
	m.selected = items[m.cursor].key()
	m.reveal()
}

// reveal scrolls the activity list so the selected row shows, with the
// heading over it when it is the first of its section.
func (m *model) reveal() {
	if m.detail || m.snap == nil {
		return
	}
	if m.above && m.cursor == 0 {
		return
	}
	m.above = false
	_, at := m.activityLines(max(1, m.width-2))
	if m.cursor >= len(at) {
		return
	}
	line, height, first := at[m.cursor], m.bodyHeight(), m.scroll[tabActivity]
	switch {
	case m.cursor == 0 && line < height:
		// The first row, with everything over it, when both fit.
		first = 0
	case line-1 < first:
		first = line - 1
	case line >= first+height:
		first = line - height + 1
	}
	m.scroll[tabActivity] = max(0, first)
}

// clampStep keeps a jump to either end from overflowing a sum.
func clampStep(by int) int {
	return min(max(by, -1<<20), 1<<20)
}

// page is how far a page key moves.
func (m model) page() int {
	return max(1, m.bodyHeight()-1)
}

// follow keeps the selection on the row it was on after the rows changed
// under it, and on the list when that row is gone.
func (m *model) follow() {
	items := m.items()
	if len(items) == 0 {
		m.cursor, m.selected, m.detail = 0, "", false
		return
	}
	for i, it := range items {
		if it.key() == m.selected {
			m.cursor = i
			m.reveal()
			return
		}
	}
	if m.selected != "" {
		// The row shown in full is gone: back to the list.
		m.detail = false
	}
	m.cursor = min(m.cursor, len(items)-1)
	m.selected = items[m.cursor].key()
	m.reveal()
}

// open opens the selected row's recording in a browser.
func (m *model) open() tea.Cmd {
	if m.opts.Open == nil || m.tab != tabActivity {
		return nil
	}
	items := m.items()
	if m.cursor >= len(items) {
		return nil
	}
	url := items[m.cursor].url()
	// Nothing the console was given is trusted to be a safe address.
	if !strings.HasPrefix(url, "https://") {
		m.note = "This one has no address in Basecamp to open."
		return nil
	}
	m.note = "Opened in your browser."
	open := m.opts.Open
	return func() tea.Msg { return openedMsg{open(url)} }
}

func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}
