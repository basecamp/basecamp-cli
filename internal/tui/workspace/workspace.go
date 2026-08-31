// Package workspace is the full-screen Basecamp terminal app.
//
// One screen is on top at a time. Home sits at the bottom of a stack, views
// push onto it, and esc pops back down — there is no row of tabs. The model
// owns everything that outlives a single screen: the palette, the help bar, the
// toast, the error box, and the stack itself. A view owns what it draws and the
// reads behind it, and answers keys the model did not claim first.
package workspace

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// ctrlCWindow is how long a first ctrl+c counts for. Quitting takes two so a
// stray one does not throw away a screen the reader was working on.
const ctrlCWindow = 2 * time.Second

type model struct {
	width  int
	height int

	ctx    *Context
	cancel context.CancelFunc

	nav     nav
	menu    menu
	sidebar sidebar
	theme   tui.Theme

	// modal is the form open over the screen, and nil when none is. It holds
	// every key while it is up.
	modal modal

	// styles is a pointer every view holds, so a theme switch reaches all of
	// them by rewriting what it points at rather than by walking the stack.
	styles *tui.Styles
	help   helpBar

	// The notifications stream and what it takes to keep it open: the attempt
	// that owns the current watch, how many opens have failed in a row, and
	// whether a re-read is already armed.
	watch         ReadingsWatcher
	changes       <-chan struct{}
	watchAttempt  uint64
	watchFailures int
	refreshDue    bool

	toast   notifyMsg
	toastID uint64

	loading      bool
	spinnerPhase int
	err          error
	ctrlCOnce    bool
}

func newModel(app *appctx.App) model {
	theme := tui.ResolveTheme(tui.DetectDark())
	styles := tui.NewStylesWithTheme(theme)
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel stored, called on quit

	viewContext := &Context{app: app, ctx: ctx, styles: styles}
	stack := newNav(newHome(viewContext))

	// With no account settled, the picker is the first thing the reader sees —
	// over home rather than instead of it, so choosing one lands them there.
	// There is nothing behind it yet, so esc has nowhere to go.
	if viewContext.AccountID() == "" {
		stack.push(newAccountPicker(viewContext, false))
	}

	return model{
		ctx:          viewContext,
		cancel:       cancel,
		nav:          stack,
		menu:         newMenu(styles),
		sidebar:      newSidebar(styles),
		theme:        theme,
		styles:       styles,
		help:         newHelpBar(styles),
		watchAttempt: 1,
	}
}

// Init only returns commands: it is handed a copy of the model, so anything it
// set would be lost. The first watch attempt is numbered by the constructor for
// that reason.
func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.nav.current().Init(), tea.RequestBackgroundColor}

	// The header wants the account by name and config holds only the id, so
	// unless the picker is about to read the accounts anyway, read them for the
	// name alone. The sidebar wants the notifications for the same account.
	if m.ctx.AccountID() != "" {
		cmds = append(cmds,
			loadAccounts(m.ctx.Ctx(), m.ctx.app),
			loadReadings(m.ctx.Ctx(), m.ctx.app, time.Now()))
	}
	return tea.Batch(append(cmds, startWatchCmd(m.ctx.Ctx(), m.watch, m.watchAttempt))...)
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.setWidth(msg.Width)
		m.relayout()
		return m, nil

	case tea.BackgroundColorMsg:
		if m.theme.Dark != msg.IsDark() {
			m.restyle(tui.ResolveTheme(msg.IsDark()))
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case notifyMsg:
		return m, m.showToast(msg)

	case toastExpiredMsg:
		if msg.id == m.toastID {
			m.toast = notifyMsg{}
		}
		return m, nil

	case spinnerTickMsg:
		if m.loading {
			m.spinnerPhase++
			return m, spinnerTick()
		}
		return m, nil

	case ctrlCResetMsg:
		m.ctrlCOnce = false
		m.relayout()
		return m, nil

	case accountsLoadedMsg:
		// The header wants the name whoever asked for the list; the picker,
		// when it is the one on screen, wants the list itself.
		m.rememberAccountName(msg)
		cmd, _ := m.nav.current().Update(msg)
		return m, m.syncLoading(cmd)

	case accountChosenMsg:
		return m, m.chooseAccount(msg.account)

	case readingsLoadedMsg:
		m.sidebar.loaded = true
		m.sidebar.notice = ""
		if msg.err != nil {
			// A sidebar that could not be read says so in its own column and
			// leaves the screen alone: it is not what the reader was doing.
			m.sidebar.notice = errorNotice("Could not load notifications", msg.err)
		} else {
			m.sidebar.replace(msg.readings)
		}
		m.relayout()
		return m, nil

	case moreReadingsLoadedMsg:
		m.sidebar.paging = false
		if msg.err != nil {
			// The rows already on screen are still good, so a page that failed
			// to arrive stops the walk rather than replacing them with a notice.
			m.sidebar.exhausted = true
			m.relayout()
			return m, nil
		}
		m.sidebar.appendReads(msg.page, msg.reads)
		m.relayout()
		return m, nil

	case projectsLoadedMsg:
		if msg.err != nil {
			// The places above them are still reachable, so a page that failed
			// to arrive says so under the list rather than closing the menu, and
			// stops the walk rather than asking again.
			m.menu.projectsPaging = false
			m.menu.projectsLoaded = true
			m.menu.projectsDone = true
			m.menu.projectsNotice = errorNotice("Could not load the projects", msg.err)
			m.relayout()
			return m, nil
		}
		m.menu.appendProjects(msg)
		m.relayout()
		return m, m.loadMenuProjects()

	case watchStartedMsg:
		if msg.attempt != m.watchAttempt {
			return m, nil
		}
		return m, m.watchStarted(msg)

	case watchChangedMsg:
		if msg.attempt != m.watchAttempt {
			return m, nil
		}
		if msg.closed {
			return m, m.retryWatch()
		}
		return m, tea.Batch(m.readingsChanged(), waitForChangeCmd(msg.attempt, m.changes))

	case watchRetryMsg:
		if msg.attempt != m.watchAttempt || m.changes != nil {
			return m, nil
		}
		return m, m.startWatch()

	case readingsRefreshDueMsg:
		return m, m.refreshReadings()

	case openAllMsg:
		return m, m.openAll(msg.kind)

	case openFolderMsg:
		return m, m.openFolder(msg.folder)

	case editFolderMsg:
		return m, m.openModal(newFolderEdit(m.ctx, msg.folder))

	case folderRenamedMsg:
		m.closeModal()
		return m, m.folderRenamed(msg)

	case folderGoneMsg:
		m.closeModal()
		// The folder is gone, so the screen showing it is a screen showing
		// nothing. Home is where it was opened from and where its row was.
		return m, tea.Batch(m.goHome(), notify("Deleted "+msg.name))

	case openProjectMsg:
		return m, m.openProject(msg.project)

	case pickerCanceledMsg:
		return m, m.pop()

	case errMsg:
		m.loading = false
		m.err = msg.err
		m.relayout()
		return m, nil
	}

	// A modal takes the answers to its own writes, and its cursor blink, before
	// the screen underneath sees them.
	if m.modal != nil {
		if cmd, took := m.modal.Update(msg); took {
			m.relayout()
			return m, m.syncLoading(cmd)
		}
	}

	cmd, _ := m.nav.current().Update(msg)
	return m, m.syncLoading(cmd)
}

// syncLoading brings the model's spinner in line with the view on top, and arms
// the tick when the view has just started waiting on something.
func (m *model) syncLoading(cmd tea.Cmd) tea.Cmd {
	m.relayout()
	loading := m.nav.current().Loading()
	if loading == m.loading {
		return cmd
	}
	m.loading = loading
	if loading {
		m.spinnerPhase = 0
		return tea.Batch(cmd, spinnerTick())
	}
	return cmd
}

// restyle makes theme the active palette. Every view holds the same *Styles, so
// rewriting it in place is all the propagation there is.
func (m *model) restyle(theme tui.Theme) {
	m.theme = theme
	m.styles.UpdateTheme(theme)
}

// --- Navigation ---

// push opens a screen over the current one and runs whatever it needs to fill
// itself in.
func (m *model) push(view View) tea.Cmd {
	m.nav.push(view)
	view.Resize(m.ctx.width, m.ctx.height)
	return m.syncLoading(view.Init())
}

// pop goes back one screen, to the state it was left in. At home there is
// nowhere to go, and esc does nothing.
func (m *model) pop() tea.Cmd {
	if !m.nav.pop() {
		return nil
	}
	m.nav.current().Resize(m.ctx.width, m.ctx.height)
	return m.syncLoading(nil)
}

// chooseAccount makes an account the one the workspace reads, for as long as it
// runs, and drops the picker. Nothing is written to disk: `basecamp config set
// account_id` is how a choice is made to stick, here and for every other
// command.
func (m *model) chooseAccount(chosen account) tea.Cmd {
	switched := chosen.id != m.ctx.AccountID()
	m.ctx.app.Config.AccountID = chosen.id
	m.ctx.accountName = chosen.name
	if m.ctx.app.Names != nil {
		m.ctx.app.Names.SetAccountID(chosen.id)
	}

	cmd := m.pop()
	if !switched {
		return cmd
	}

	// Everything on screen belonged to the account that was open a moment ago,
	// so it all reads again. The screen underneath is one of them: popping back
	// to it hands it over in the state it was left, which is the right thing
	// when the reader walked back — and the wrong thing when the ground moved.
	m.sidebar.loaded = false
	m.sidebar.readings = readings{}
	m.menu.forgetProjects()

	return tea.Batch(
		cmd,
		m.nav.current().Init(),
		m.refreshReadings(),
		notify("Opened "+chosen.name),
	)
}

// rememberAccountName picks the open account's name out of a list read for
// whatever reason, so the header can stop showing its id.
func (m *model) rememberAccountName(msg accountsLoadedMsg) {
	if msg.err != nil {
		return
	}
	for _, a := range msg.accounts {
		if a.id == m.ctx.AccountID() {
			m.ctx.accountName = a.name
			return
		}
	}
}

// handleSidebarKey walks the notifications while the sidebar has focus, and
// answers whether it took the key.
func (m *model) handleSidebarKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch {
	case msg.String() == sidebarLeaveKey, msg.Key().Code == tea.KeyEscape:
		m.sidebar.leave()
		m.relayout()
		return nil, true
	case msg.Key().Code == tea.KeyUp:
		return m.moveSidebarCursor(-1), true
	case msg.Key().Code == tea.KeyDown:
		return m.moveSidebarCursor(1), true
	}
	return nil, false
}

// moveSidebarCursor walks the list, and asks for the next page of previous
// notifications when the reader gets near the end of what is there.
func (m *model) moveSidebarCursor(by int) tea.Cmd {
	if !m.sidebar.moveCursor(by) {
		return nil
	}
	m.sidebar.paging = true
	return loadMorePreviousNotifications(
		m.ctx.Ctx(), m.ctx.app, nextPage(m.sidebar.page), time.Now())
}

// openSection goes to one of the top-level destinations. They are siblings, not
// a ladder, so this comes back to home before opening one: the trail reads
// Home › Calendar however deep the reader was when they pressed the key.
func (m *model) openSection(chosen section) tea.Cmd {
	if m.nav.depth() == 2 && m.nav.current().Title() == chosen.label {
		return nil
	}
	// Focus follows the reader: they asked for a screen, so that is where the
	// keys should go.
	m.sidebar.leave()
	m.popToHome()
	return m.push(newBlank(m.ctx, chosen.label))
}

// openProject goes to a project the menu or the home screen listed. Projects
// hang off home the way the sections do, so the trail reads
// Home › Website redesign.
func (m *model) openProject(chosen project) tea.Cmd {
	m.sidebar.leave()
	m.popToHome()
	return m.push(newBlank(m.ctx, chosen.name))
}

// openAll goes to one of the two screens the home screen's buttons lead to: the
// whole activity feed, or every project. Both hang off home the way a project
// does, so the trail reads Home › All projects.
func (m *model) openAll(kind listKind) tea.Cmd {
	m.sidebar.leave()
	m.popToHome()
	if kind == listActivity {
		return m.push(newActivity(m.ctx))
	}
	return m.push(newAllProjects(m.ctx))
}

// openFolder goes to one of the home screen's folders, which hangs off home the
// same way a project does.
func (m *model) openFolder(chosen folder) tea.Cmd {
	m.sidebar.leave()
	m.popToHome()
	return m.push(newFolder(m.ctx, chosen))
}

// folderRenamed carries the new name to the screen showing the folder, so the
// breadcrumb and the heading say what the folder is now called without reading
// it again. Home is behind it and holds the old name too, so it re-reads.
func (m *model) folderRenamed(msg folderRenamedMsg) tea.Cmd {
	if open, ok := m.nav.current().(*listScreen); ok && open.inside != nil {
		open.inside.name = msg.name
	}
	m.relayout()
	return tea.Batch(m.refreshHome(), notify("Renamed to "+msg.name))
}

// refreshHome reads the home screen again, for when something it lists has
// changed underneath it.
func (m *model) refreshHome() tea.Cmd {
	if at, ok := m.nav.at(0).(*home); ok {
		return at.Init()
	}
	return nil
}

// loadMenuProjects asks for the next page of projects when the menu wants one:
// on the first open, when the cursor nears the end of what is loaded, and when
// there are not enough entries to fill the box. Never more than a page at a
// time, and never a page nobody has scrolled far enough to see.
func (m *model) loadMenuProjects() tea.Cmd {
	if !m.menu.wantsMoreProjects() {
		return nil
	}
	m.menu.projectsPaging = true
	return loadProjects(m.ctx.Ctx(), m.ctx.app, m.menu.projectsPage+1)
}

// goHome unwinds the stack in one step, however deep the reader has walked.
func (m *model) goHome() tea.Cmd {
	if m.nav.depth() == 1 && !m.sidebar.focused {
		return nil
	}
	m.sidebar.leave()
	m.popToHome()
	return m.syncLoading(nil)
}

func (m *model) popToHome() {
	for m.nav.pop() {
	}
}

// openAccountPicker brings the picker back to switch accounts, unless it is
// already the screen the reader is on.
func (m *model) openAccountPicker() tea.Cmd {
	if _, open := m.nav.current().(*accountPicker); open {
		return nil
	}
	return m.push(newAccountPicker(m.ctx, true))
}

// --- Key handling ---

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.help.notice != "" {
		m.help.setNotice("")
	}

	if key == "ctrl+c" {
		if m.ctrlCOnce {
			m.cancel()
			return m, tea.Quit
		}
		m.ctrlCOnce = true
		m.relayout()
		return m, tea.Tick(ctrlCWindow, func(time.Time) tea.Msg { return ctrlCResetMsg{} })
	}
	if m.ctrlCOnce {
		m.ctrlCOnce = false
		m.relayout()
	}

	// The error box stands over the screen until it is dismissed; nothing else
	// should act on a screen it is covering.
	if m.err != nil {
		if msg.Key().Code == tea.KeyEscape || key == "q" {
			m.err = nil
			m.relayout()
		}
		return m, nil
	}

	// A modal stands over everything, the menu included: it is what the reader
	// is doing, so it answers first and keeps every key.
	if m.modal != nil {
		cmd, stays := m.modal.HandleKey(msg)
		if !stays {
			m.closeModal()
			return m, m.syncLoading(cmd)
		}
		m.relayout()
		return m, m.syncLoading(cmd)
	}

	// The menu stands over the screen while it is up, so it answers first.
	if m.menu.open {
		if key == menuKey || key == menuAltKey {
			m.menu.close()
			m.relayout()
			return m, nil
		}
		cmd := m.menu.handleKey(&m, msg)
		m.relayout()
		// Walking down the list, or narrowing it to a handful of matches, is
		// what asks for the page below what is loaded.
		return m, m.syncLoading(tea.Batch(cmd, m.loadMenuProjects()))
	}

	// A view with an open text field gets every key — esc, tab, letters and all.
	// The section numbers included: a digit typed into a search box is search
	// text, not a jump.
	if capturer, ok := m.nav.current().(inputCapturer); ok && capturer.CapturingInput() {
		return m, m.syncLoading(m.nav.current().HandleKey(msg))
	}

	// The sidebar takes the keys that walk it while it has focus. Everything
	// else falls through, so the menu and the section keys still work from over
	// there — and going somewhere hands focus back to the screen it lands on.
	if m.sidebar.focused {
		if cmd, claimed := m.handleSidebarKey(msg); claimed {
			return m, cmd
		}
	}

	if key == menuKey || key == menuAltKey {
		m.menu.toggle()
		m.relayout()
		return m, m.loadMenuProjects()
	}

	// The numbers reach their sections with the menu shut. That is the point of
	// the menu: it shows the keys until the reader stops needing it.
	if chosen, ok := sectionForKey(key); ok {
		return m, m.openSection(chosen)
	}

	if key == homeKey {
		return m, m.goHome()
	}

	if key == sidebarKey {
		m.sidebar.summon()
		m.relayout()
		return m, nil
	}

	if key == "ctrl+a" {
		return m, m.openAccountPicker()
	}

	if key == "?" {
		m.help.setHidden(!m.help.hidden)
		m.relayout()
		return m, nil
	}

	if msg.Key().Code == tea.KeyEscape || key == "q" {
		if blocker, ok := m.nav.current().(popBlocker); ok && blocker.HandleBack() {
			m.relayout()
			return m, nil
		}
		return m, m.pop()
	}

	if m.loading {
		return m, nil
	}

	return m, m.syncLoading(m.nav.current().HandleKey(msg))
}

// --- View ---

func (m model) View() tea.View {
	var b strings.Builder
	b.WriteString(renderHeader(&m))
	b.WriteString("\n")
	b.WriteString(m.contentView())

	if help := m.help.view(); help != "" {
		drawn := strings.Count(b.String(), "\n")
		footer := 1 + strings.Count(help, "\n") + 1
		for range max(m.height-drawn-footer-1, 0) {
			b.WriteString("\n")
		}
		b.WriteString(renderRule(m.width, m.styles, ""))
		b.WriteString("\n" + help)
	}

	view := tea.NewView(m.withModal(m.withMenu(b.String())))
	view.AltScreen = true
	return view
}

// withMenu hangs the menu one row under the top line, so the account and its
// caret stay on screen above it — the caret turning over is what says the menu
// is open.
//
// It is drawn over everything, centered on the terminal rather than on either
// column: the reader opened it from the caret above it, and that is where they
// are looking.
//
// Only the box itself is painted. The workspace stays where it was underneath —
// the sidebar included, which the box crosses and so cuts the middle out of. A
// line that runs under a panel reading as a line that runs under a panel is the
// trade for not blanking half the screen every time the menu opens.
func (m model) withMenu(rendered string) string {
	dropdown := m.menu.view()
	if dropdown == "" {
		return rendered
	}
	x := max((m.width-tui.DisplayWidth(dropdown))/2, 0)
	return overlayAt(rendered, dropdown, x, menuTopRow, m.width, lipgloss.Height(rendered))
}

// contentView is the content area on its own, which is what an overlay draws
// itself over.
func (m model) contentView() string {
	height := m.contentHeight()
	content := lipgloss.NewStyle().Width(m.contentWidth()).Height(height).Render(m.screenView())

	if width := m.sidebarWidth(); width > 0 {
		content = lipgloss.JoinHorizontal(lipgloss.Top,
			content,
			m.sidebar.gutter(m.styles, height),
			m.sidebar.view())
	}

	// The toast goes on last, over everything — the sidebar included: it is the
	// answer to what the reader just did, and a box open over the screen does
	// not make it less so.
	if toast := m.toastView(); toast != "" {
		x := max(m.width-tui.DisplayWidth(toast)-1, 0)
		content = overlayAt(content, toast, x, 0, m.width, height)
	}
	return content
}

// screenView is the content column on its own: the screen on top, or whatever
// the frame has put over it.
func (m model) screenView() string {
	width, height := m.contentWidth(), m.contentHeight()
	switch {
	case m.err != nil:
		// The view keeps its last good state underneath, so dismissing the
		// error with esc puts the reader back where they were.
		message := richtext.SanitizeSingleLine(m.err.Error())
		box := errorView(message, width, m.styles)
		return overlayCentered(m.nav.current().View(), box, width, height)
	case m.loading:
		return loadingView(width, height, m.spinnerPhase, m.styles)
	default:
		return m.nav.current().View()
	}
}

// contentHeight is every row that is not header or a visible help footer. The
// footer carries a blank row above its rule.
func (m model) contentHeight() int {
	return max(m.height-headerHeight-m.footerHeight(), 1)
}

func (m model) footerHeight() int {
	if height := m.help.height(); height > 0 {
		return height + 2
	}
	return 0
}

// menuRows is how many rows of its list the menu may show: never so many that it
// reaches the help bar, and never more than half the screen. It is a panel over
// the workspace, and a panel that fills the terminal is just a screen.
func (m model) menuRows() int {
	room := m.height - menuTopRow - menuChrome - m.footerHeight() - 1
	half := m.height * menuMaxRowsNumerator / menuMaxRowsDenominator
	return max(min(room, half), menuMinRows)
}

// relayout rebuilds the help bar and hands the view on top whatever the frame
// leaves it. Everything goes through here rather than through updateHelp,
// because the bar is what decides the height: a screen whose bindings wrap onto
// a second line has one row less to draw in than one whose bindings fit.
//
// The width it hands over is the content column's, not the terminal's. A screen
// draws into what it is given and never assumes it has the whole width — the
// sidebar takes its share, and can appear or vanish under a screen that is
// already open.
func (m *model) relayout() {
	m.updateHelp()
	m.ctx.width = m.contentWidth()
	m.ctx.height = m.contentHeight()
	m.nav.current().Resize(m.ctx.width, m.ctx.height)

	m.menu.resize(m.width, m.menuRows())

	if m.modal != nil {
		m.modal.Resize(max(modalWidth(m.width)-modalChromeWidth, 1),
			max(m.contentHeight()-modalChromeRows, 1))
	}

	// A sidebar nobody can see cannot be the focused one, so a screen that wants
	// the whole terminal takes focus back with it.
	if width := m.sidebarWidth(); width > 0 {
		m.sidebar.resize(width, m.contentHeight())
	} else {
		m.sidebar.focused = false
	}
}

// contentWidth is the columns left for the screen once the sidebar and the
// gutter between them have taken theirs.
func (m model) contentWidth() int {
	if width := m.sidebarWidth(); width > 0 {
		return max(m.width-sidebarGutter-width, 1)
	}
	return max(m.width, 1)
}

// sidebarWidth is the sidebar's columns on this screen: none when it is hidden,
// when the terminal is too narrow for both, or when the screen wants the whole
// terminal to itself.
func (m model) sidebarWidth() int {
	if !m.sidebarAvailable() {
		return 0
	}
	return m.sidebar.columns(m.width)
}

// sidebarAvailable is whether the frame has a sidebar to talk about at all,
// which is what decides whether the divider mentions it. Hiding it with the key
// does not make it unavailable — the hint has to stay for the reader to find it
// again.
func (m model) sidebarAvailable() bool {
	if wide, ok := m.nav.current().(fullWidth); ok && wide.WantsFullWidth() {
		return false
	}
	return m.sidebar.fits(m.width)
}

// updateHelp rebuilds the bar: what the model itself answers for, then what the
// view on top adds.
func (m *model) updateHelp() {
	quit := helpBinding{"ctrl+c ctrl+c", "quit"}
	if m.ctrlCOnce {
		quit = helpBinding{"ctrl+c", "press again to quit"}
	}

	switch {
	case m.err != nil:
		m.help.setBindings([]helpBinding{{"esc/q", "dismiss"}, quit})
		return
	case m.modal != nil:
		m.help.setBindings(append(m.modal.HelpBindings(), quit))
		return
	case m.menu.open:
		m.help.setBindings(append(m.menu.helpBindings(), quit))
		return
	case m.capturingInput():
		m.help.setBindings(append(m.nav.current().HelpBindings(), quit))
		return
	case m.sidebar.focused:
		m.help.setBindings([]helpBinding{
			{"↑↓", "notification"},
			{sidebarLeaveKey, "back to screen"},
			quit,
		})
		return
	}

	bindings := m.nav.current().HelpBindings()
	if m.nav.depth() > 1 {
		bindings = append(bindings, helpBinding{"esc/q", "back"})
	}
	bindings = append(bindings, helpBinding{"ctrl+a", "account"}, quit)
	if !m.help.hidden {
		bindings = append(bindings, helpBinding{"?", "hide help"})
	}
	m.help.setBindings(bindings)
}

func (m model) capturingInput() bool {
	capturer, ok := m.nav.current().(inputCapturer)
	return ok && capturer.CapturingInput()
}

// New builds the workspace and the shutdown that cancels whatever it still has
// in flight. Starting the Bubble Tea program is the caller's job: a launcher
// reached without an interactivity floor waits on /dev/tty rather than failing,
// so the one place that reaches it stays behind the dev build tag — see
// TestNoUnsanctionedLaunchers.
func New(app *appctx.App, options ...Option) (tea.Model, func()) {
	m := newModel(app)
	for _, option := range options {
		option(&m)
	}
	return m, m.cancel
}

// Option configures the workspace at startup.
type Option func(*model)

// WithReadingsWatcher hands the workspace the stream that says its notifications
// changed. Without one the sidebar is the snapshot it was read as.
func WithReadingsWatcher(watch ReadingsWatcher) Option {
	return func(m *model) { m.watch = watch }
}
