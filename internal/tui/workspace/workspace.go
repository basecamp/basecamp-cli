package workspace

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/chrome"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// chromeHeight returns the vertical space reserved for breadcrumb + divider + status bar.
func (w *Workspace) chromeHeight() int {
	return 3 // breadcrumb + divider + status bar
}

// poolMonitorWidth is the fixed width of the pool monitor right sidebar.
const poolMonitorWidth = 38

// minMainWidth is the minimum width for the main content area.
const minMainWidth = 40

// Workspace is the root tea.Model for the persistent TUI application.
type Workspace struct {
	session  *Session
	router   *Router
	styles   *tui.Styles
	keys     GlobalKeyMap
	registry *Registry

	// Chrome
	statusBar       chrome.StatusBar
	breadcrumb      chrome.Breadcrumb
	toast           chrome.Toast
	help            chrome.Help
	palette         chrome.Palette
	accountSwitcher chrome.AccountSwitcher
	boostPicker     *BoostPicker
	pickingBoost    bool
	boostTarget     BoostTarget
	quickJump       chrome.QuickJump

	// Multi-account
	accountList []AccountInfo

	// Sidebar
	sidebarView    View
	sidebarTargets []ViewTarget // cycle order
	sidebarIndex   int          // current position in cycle (-1 = closed)
	sidebarRatio   float64      // left panel ratio (0.30 default)
	showSidebar    bool
	sidebarFocused bool

	// Pool monitor (right sidebar)
	poolMonitor        View
	showPoolMonitor    bool
	poolMonitorFocused bool

	// State
	showHelp            bool
	showPalette         bool
	showAccountSwitcher bool
	showQuickJump       bool
	quitting            bool
	confirmQuit         bool
	windowTitle         string

	// Theme file watcher for live reloading
	themeWatcher *fsnotify.Watcher

	// Ambient digest polling (feeds sidebar and views)
	digestPollGen uint64

	// ViewFactory builds views from targets — set by the command that creates the workspace.
	viewFactory        ViewFactory
	poolMonitorFactory func() View // creates the pool monitor view
	openFunc           func(Scope) tea.Cmd

	// createBoostFunc is the function called to create a boost. Defaults to
	// createBoost; tests can replace it with a spy.
	createBoostFunc func(BoostTarget, string) tea.Cmd

	width, height int
}

// ViewFactory creates views for navigation targets.
type ViewFactory func(target ViewTarget, session *Session, scope Scope) View

// New creates a new Workspace model.
func New(session *Session, factory ViewFactory, poolMonitorFactory func() View) *Workspace {
	styles := session.Styles()
	registry := DefaultActions()

	keys := DefaultGlobalKeyMap()
	if configDir, err := os.UserConfigDir(); err == nil {
		overrides, err := LoadKeyOverrides(filepath.Join(filepath.Clean(configDir), "basecamp", "keybindings.json"))
		if err != nil {
			log.Printf("keybindings: %v", err)
		}
		if len(overrides) > 0 {
			ApplyOverrides(&keys, overrides)
		}
	}

	w := &Workspace{
		session:            session,
		router:             NewRouter(),
		styles:             styles,
		keys:               keys,
		registry:           registry,
		statusBar:          chrome.NewStatusBar(styles),
		breadcrumb:         chrome.NewBreadcrumb(styles),
		toast:              chrome.NewToast(styles),
		help:               chrome.NewHelp(styles),
		palette:            chrome.NewPalette(styles),
		accountSwitcher:    chrome.NewAccountSwitcher(styles),
		quickJump:          chrome.NewQuickJump(styles),
		boostPicker:        NewBoostPicker(styles),
		viewFactory:        factory,
		poolMonitorFactory: poolMonitorFactory,
		openFunc:           openInBrowser,
		sidebarTargets:     defaultSidebarTargets(session),
		sidebarIndex:       -1,
		sidebarRatio:       0.30,
	}
	w.createBoostFunc = w.createBoost

	return w
}

// Init implements tea.Model.
func (w *Workspace) Init() tea.Cmd {
	// Create and push the initial view (home dashboard)
	scope := w.session.Scope()

	// Ensure the account realm is ready before any views fetch data.
	if w.session.HasAccount() {
		w.session.Hub().EnsureAccount(scope.AccountID)
	}

	view := w.viewFactory(ViewHome, w.session, scope)
	w.router.Push(view, scope, ViewHome)
	w.syncChrome()

	cmds := []tea.Cmd{
		w.stampCmd(view.Init()),
		chrome.SetTerminalTitle("basecamp"),
		func() tea.Msg { return tea.RequestBackgroundColor() },
	}

	// Deep-link: if a URL was passed via CLI args, navigate there after Home init.
	if target, deepScope, ok := w.session.ConsumeInitialView(); ok {
		// Merge account from session scope when the deep-link scope carries one.
		if deepScope.AccountID == "" {
			deepScope.AccountID = scope.AccountID
		}
		cmds = append(cmds, Navigate(target, deepScope))
	}

	// Fetch account name asynchronously
	if w.session.HasAccount() {
		cmds = append(cmds, w.stampCmd(w.fetchAccountName()))
	}

	// Discover all accounts for multi-account features
	cmds = append(cmds, w.discoverAccounts())

	// Watch theme file for live reloading
	if cmd := w.startThemeWatcher(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	return tea.Batch(cmds...)
}

func (w *Workspace) discoverAccounts() tea.Cmd {
	ms := w.session.MultiStore()
	// Use the Hub's global realm context: survives account switches,
	// canceled only on shutdown. Discovery is identity-wide, not account-scoped.
	ctx := w.session.Hub().Global().Context()
	return func() tea.Msg {
		accounts, err := ms.DiscoverAccounts(ctx)
		if err != nil {
			return AccountsDiscoveredMsg{Err: err}
		}
		infos := make([]AccountInfo, len(accounts))
		for i, a := range accounts {
			infos[i] = AccountInfo{ID: a.ID, Name: a.Name}
		}
		return AccountsDiscoveredMsg{Accounts: infos}
	}
}

func (w *Workspace) fetchAccountName() tea.Cmd {
	// Capture the account ID at dispatch time so the handler can reject
	// stale results if the account changed (defense-in-depth beyond epoch guard).
	accountID := w.session.Scope().AccountID
	return func() tea.Msg {
		ctx := w.session.Context()
		accounts, err := w.session.App().Resolve().SDK().Authorization().GetInfo(ctx, nil)
		if err != nil {
			return AccountNameMsg{AccountID: accountID, Err: err}
		}
		for _, acct := range accounts.Accounts {
			if fmt.Sprintf("%d", acct.ID) == accountID {
				return AccountNameMsg{AccountID: accountID, Name: acct.Name}
			}
		}
		return AccountNameMsg{AccountID: accountID, Name: accountID} // fallback to ID
	}
}

// startDigestPoll kicks off the first BonfireDigest fetch and arms a recurring poll.
// BonfireDigest is self-sufficient: it fetches BonfireRooms inline when needed.
// This makes the digest ambient — it refreshes regardless of which view is active,
// feeding the bonfire sidebar and any views that consume digest data.
func (w *Workspace) startDigestPoll() tea.Cmd {
	if !w.bonfireEnabled() {
		return nil
	}
	hub := w.session.Hub()
	if hub == nil {
		return nil
	}
	ctx := hub.Global().Context()
	return tea.Batch(
		hub.BonfireDigest().Fetch(ctx),
		w.scheduleDigestPoll(),
	)
}

func (w *Workspace) scheduleDigestPoll() tea.Cmd {
	w.digestPollGen++
	gen := w.digestPollGen
	return tea.Tick(15*time.Second, func(t time.Time) tea.Msg {
		return data.PollMsg{Tag: "workspace-digest", Gen: gen}
	})
}

// Update implements tea.Model.
func (w *Workspace) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		w.relayout()
		// If the pool monitor was focused but resize made it inactive,
		// return focus to the main view so it resumes polling/input.
		if w.poolMonitorFocused && !w.poolMonitorActive() {
			w.poolMonitorFocused = false
			updated, _ := w.poolMonitor.Update(BlurMsg{})
			w.poolMonitor = updated
			if view := w.router.Current(); view != nil {
				updated, cmd := view.Update(FocusMsg{})
				w.replaceCurrentView(updated)
				return w, w.stampCmd(cmd)
			}
		}
		return w, nil

	case tea.BackgroundColorMsg:
		w.session.SetDarkBackground(msg.IsDark())
		w.session.ReloadTheme()
		return w, nil

	case tea.FocusMsg:
		if hub := w.session.Hub(); hub != nil {
			hub.SetTerminalFocused(true)
		}
		// Forward to current view and sidebar so polling views can reschedule
		// at the new (faster) interval instead of waiting out the prior 4× timer.
		var cmds []tea.Cmd
		if view := w.router.Current(); view != nil {
			updated, c := view.Update(TerminalFocusMsg{})
			w.replaceCurrentView(updated)
			cmds = append(cmds, w.stampCmd(c))
		}
		if w.sidebarActive() {
			updated, c := w.sidebarView.Update(TerminalFocusMsg{})
			w.sidebarView = updated
			cmds = append(cmds, w.stampCmd(c))
		}
		return w, tea.Batch(cmds...)

	case tea.BlurMsg:
		if hub := w.session.Hub(); hub != nil {
			hub.SetTerminalFocused(false)
		}
		return w, nil

	case tea.KeyPressMsg:
		return w, w.handleKey(msg)

	case EpochMsg:
		if msg.Epoch != w.session.Epoch() {
			return w, nil // stale — discard
		}
		return w.Update(msg.Inner)

	case AccountNameMsg:
		// Reject if the account changed since this fetch was dispatched
		// (defense-in-depth beyond epoch guard).
		if msg.AccountID != "" && msg.AccountID != w.session.Scope().AccountID {
			return w, nil
		}
		name := msg.Name
		if name == "" && msg.Err != nil {
			// Fallback: show account ID when name lookup fails
			name = w.session.Scope().AccountID
		}
		if name != "" {
			w.statusBar.SetAccount(name)
			scope := w.session.Scope()
			scope.AccountName = name
			w.session.SetScope(scope)
			w.syncAccountBadge(w.router.CurrentTarget())
		}
		return w, nil

	case AccountsDiscoveredMsg:
		if msg.Err != nil {
			return w, SetStatus("Account discovery failed", true)
		}
		w.accountList = msg.Accounts
		w.syncAccountBadge(w.router.CurrentTarget())
		w.syncChrome() // refresh global hints (ctrl+a visibility)

		var cmds []tea.Cmd

		// Invalidate bonfire rooms so they re-fetch with all accounts,
		// then start ambient digest polling for the sidebar.
		if w.bonfireEnabled() {
			hub := w.session.Hub()
			if hub != nil {
				hub.BonfireRooms().Invalidate()
			}
			cmds = append(cmds, w.startDigestPoll())
		}

		// Refresh Home/Projects after discovery completes. This handles:
		// - Multi-account: views switch to cross-account fan-out mode.
		// - Single-account: identity is now available for identity-dependent
		//   pools (Assignments), replacing bootstrap-empty data.
		if view := w.router.Current(); view != nil {
			target := w.router.CurrentTarget()
			if target == ViewHome || target == ViewProjects {
				updated, cmd := view.Update(RefreshMsg{})
				w.replaceCurrentView(updated)
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
		return w, tea.Batch(cmds...)

	case BoostSelectedMsg:
		w.pickingBoost = false
		w.boostPicker.Blur()
		return w, w.createBoostFunc(w.boostTarget, msg.Emoji)

	case OpenBoostPickerMsg:
		w.pickingBoost = true
		w.boostTarget = msg.Target
		w.boostPicker.Focus()
		return w, nil

	case chrome.WindowTitleMsg:
		w.windowTitle = msg.Title
		return w, nil

	case ThemeChangedMsg:
		w.session.ReloadTheme()
		// Re-arm the watcher. Re-resolve symlinks in case the target changed.
		if w.themeWatcher != nil {
			path := tui.ThemeFilePath()
			if path != "" {
				resolved, err := filepath.EvalSymlinks(path)
				if err == nil {
					// If symlink now points to a different directory, update the watcher
					newDir := filepath.Dir(resolved)
					_ = w.themeWatcher.Add(newDir) // no-op if already watching
					return w, waitForThemeChange(w.themeWatcher, resolved)
				}
			}
		}
		return w, nil

	case NavigateMsg:
		return w, w.navigate(msg.Target, msg.Scope)

	case NavigateBackMsg:
		return w, w.goBack()

	case NavigateToDepthMsg:
		return w, w.goToDepth(msg.Depth)

	case StatusMsg:
		w.statusBar.SetStatus(msg.Text, msg.IsError)
		gen := w.statusBar.StatusGen()
		return w, tea.Tick(4*time.Second, func(time.Time) tea.Msg {
			return StatusClearMsg{Gen: gen}
		})

	case StatusClearMsg:
		if msg.Gen == w.statusBar.StatusGen() {
			w.statusBar.ClearStatus()
		}
		return w, nil

	case ErrorMsg:
		if isAuthError(msg.Err) {
			w.statusBar.SetStatus("Session expired — run: basecamp auth login", true)
			return w, nil
		}
		return w, w.toast.Show(msg.Context+": "+humanizeError(msg.Err), true)

	case data.PoolUpdatedMsg:
		// Refresh status bar metrics on every pool update
		if hub := w.session.Hub(); hub != nil {
			summary := hub.Metrics().Summary()
			w.statusBar.SetMetrics(&chrome.PoolMetricsSummary{
				ActivePools: summary.ActivePools,
				P50Latency:  summary.P50Latency,
				ErrorRate:   summary.ErrorRate,
			})
		}
		var extraCmds []tea.Cmd
		// Forward to left sidebar if active
		if w.sidebarActive() {
			updated, sc := w.sidebarView.Update(msg)
			w.sidebarView = updated
			if sc != nil {
				extraCmds = append(extraCmds, w.stampCmd(sc))
			}
		}
		// Forward to pool monitor if active
		if w.poolMonitorActive() {
			updated, mc := w.poolMonitor.Update(msg)
			w.poolMonitor = updated
			if mc != nil {
				extraCmds = append(extraCmds, w.stampCmd(mc))
			}
		}
		// Forward to current view
		if view := w.router.Current(); view != nil {
			updated, cmd := view.Update(msg)
			w.replaceCurrentView(updated)
			if len(extraCmds) > 0 {
				return w, tea.Batch(append([]tea.Cmd{w.stampCmd(cmd)}, extraCmds...)...)
			}
			return w, w.stampCmd(cmd)
		}
		return w, tea.Batch(extraCmds...)

	case RefreshMsg:
		if w.statusBar.HasPersistentError() {
			w.statusBar.ClearStatus()
		}
		if view := w.router.Current(); view != nil {
			updated, cmd := view.Update(msg)
			w.replaceCurrentView(updated)
			return w, w.stampCmd(cmd)
		}

	case ChromeSyncMsg:
		w.syncChrome()
		return w, nil

	case chrome.PaletteCloseMsg:
		w.showPalette = false
		w.palette.Blur()
		return w, nil

	case chrome.PaletteExecMsg:
		if msg.Cmd != nil {
			return w, w.stampCmd(msg.Cmd)
		}
		return w, nil

	case chrome.AccountSwitchedMsg:
		w.showAccountSwitcher = false
		w.accountSwitcher.Blur()
		if msg.AccountID == "" {
			// "All Accounts" — navigate to Home with a clean scope
			return w, w.navigate(ViewHome, Scope{})
		}
		return w, w.switchAccount(msg.AccountID, msg.AccountName)

	case chrome.AccountSwitchCloseMsg:
		w.showAccountSwitcher = false
		w.accountSwitcher.Blur()
		return w, nil

	case chrome.QuickJumpCloseMsg:
		w.showQuickJump = false
		w.quickJump.Blur()
		return w, nil

	case chrome.QuickJumpExecMsg:
		if msg.Cmd != nil {
			return w, w.stampCmd(msg.Cmd)
		}
		return w, nil
	}

	// Forward non-key messages to account switcher when active
	if w.showAccountSwitcher {
		if cmd := w.accountSwitcher.Update(msg); cmd != nil {
			return w, cmd
		}
		return w, nil
	}

	// Toast ticks
	if cmd := w.toast.Update(msg); cmd != nil {
		return w, cmd
	}

	// Handle workspace-owned ambient digest poll.
	// Refreshes both rooms (for bookmark/override changes) and digest.
	if pm, ok := msg.(data.PollMsg); ok && pm.Tag == "workspace-digest" && pm.Gen == w.digestPollGen {
		hub := w.session.Hub()
		if hub != nil {
			ctx := hub.Global().Context()
			return w, tea.Batch(
				hub.BonfireRooms().FetchIfStale(ctx),
				hub.BonfireDigest().FetchIfStale(ctx),
				w.scheduleDigestPoll(),
			)
		}
		return w, w.scheduleDigestPoll()
	}

	// Forward PollMsg to sidebar alongside the main view
	// (PoolUpdatedMsg is handled by the explicit case above)
	var sidebarCmd tea.Cmd
	if w.sidebarActive() {
		if _, ok := msg.(data.PollMsg); ok {
			updated, sc := w.sidebarView.Update(msg)
			w.sidebarView = updated
			sidebarCmd = w.stampCmd(sc)
		}
	}

	// Forward to current view
	if view := w.router.Current(); view != nil {
		updated, cmd := view.Update(msg)
		w.replaceCurrentView(updated)
		if sidebarCmd != nil {
			return w, tea.Batch(w.stampCmd(cmd), sidebarCmd)
		}
		return w, w.stampCmd(cmd)
	}

	return w, sidebarCmd
}

func (w *Workspace) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	// Help overlay consumes all keys when active
	if w.pickingBoost {
		switch msg.Code { //nolint:exhaustive // partial key handler
		case tea.KeyEscape:
			w.pickingBoost = false
			w.boostPicker.Blur()
			return nil
		}
		var cmd tea.Cmd
		w.boostPicker, cmd = w.boostPicker.Update(msg)
		return cmd
	}

	if w.showHelp {
		shouldClose, cmd := w.help.Update(msg)
		if shouldClose {
			w.showHelp = false
			w.help.ResetScroll()
		}
		return cmd
	}

	// Command palette consumes keys when active
	if w.showPalette {
		return w.stampCmd(w.palette.Update(msg))
	}

	// Account switcher consumes keys when active
	if w.showAccountSwitcher {
		return w.accountSwitcher.Update(msg)
	}

	// Quick-jump consumes keys when active
	if w.showQuickJump {
		return w.stampCmd(w.quickJump.Update(msg))
	}

	// When a view is capturing text input, only allow ctrl-chord globals
	// (ctrl+p, ctrl+a, ctrl+y, ctrl+s). Skip single-key globals (q, r, ?, /, 1-9)
	// so they reach the view's text input.
	inputActive := false
	if view := w.router.Current(); view != nil {
		if ic, ok := view.(InputCapturer); ok {
			inputActive = ic.InputActive()
		}
	}

	if inputActive {
		// ctrl+c always quits, even during input capture
		if msg.String() == "ctrl+c" {
			w.quitting = true
			return tea.Quit
		}
		// Only ctrl-chord globals work during input capture
		switch {
		case key.Matches(msg, w.keys.Palette):
			return w.openPalette()
		case key.Matches(msg, w.keys.AccountSwitch):
			return w.openAccountSwitcher()
		case key.Matches(msg, w.keys.Hey):
			if w.router.CurrentTarget() != ViewHey {
				return w.navigate(ViewHey, w.session.Scope())
			}
		case key.Matches(msg, w.keys.MyStuff):
			if w.router.CurrentTarget() != ViewMyStuff {
				return w.navigate(ViewMyStuff, w.session.Scope())
			}
		case key.Matches(msg, w.keys.Activity):
			if w.router.CurrentTarget() != ViewActivity {
				return w.navigate(ViewActivity, w.session.Scope())
			}
		case key.Matches(msg, w.keys.Sidebar):
			return w.toggleSidebar()
		case key.Matches(msg, w.keys.Jump):
			return w.openQuickJump()
		case key.Matches(msg, w.keys.Bonfire):
			if w.bonfireEnabled() && !w.isBonfireView() {
				return w.navigate(ViewFrontPage, w.session.Scope())
			}
		case key.Matches(msg, w.keys.Metrics):
			return w.togglePoolMonitor()
		case key.Matches(msg, w.keys.SidebarFocus):
			if w.sidebarActive() || w.poolMonitorActive() {
				return w.switchSidebarFocus()
			}
		}
		// Forward everything else to the view
		if view := w.router.Current(); view != nil {
			updated, cmd := view.Update(msg)
			w.replaceCurrentView(updated)
			return w.stampCmd(cmd)
		}
		return nil
	}

	// Reset quit confirmation on any key that isn't a Back binding
	if w.confirmQuit && !key.Matches(msg, w.keys.Back) {
		w.confirmQuit = false
	}

	// Global keys (only when NOT in input mode)
	switch {
	case key.Matches(msg, w.keys.Quit):
		w.quitting = true
		return tea.Quit

	case key.Matches(msg, w.keys.Help):
		w.showHelp = true
		return nil

	case key.Matches(msg, w.keys.Back):
		// Pop non-main panel focus before back-navigating.
		if w.poolMonitorFocused {
			blurCmd := w.clearPoolMonitorFocus()
			if view := w.router.Current(); view != nil {
				updated, cmd := view.Update(FocusMsg{})
				w.replaceCurrentView(updated)
				return tea.Batch(blurCmd, w.stampCmd(cmd))
			}
			return blurCmd
		}
		if w.sidebarFocused {
			w.sidebarFocused = false
			if w.sidebarView != nil {
				updated, _ := w.sidebarView.Update(BlurMsg{})
				w.sidebarView = updated
			}
			if view := w.router.Current(); view != nil {
				updated, cmd := view.Update(FocusMsg{})
				w.replaceCurrentView(updated)
				return w.stampCmd(cmd)
			}
			return nil
		}
		// If the view has a modal state (move mode, results focus), let it handle Esc first
		if view := w.router.Current(); view != nil {
			if ma, ok := view.(ModalActive); ok && ma.IsModal() {
				updated, cmd := view.Update(msg)
				w.replaceCurrentView(updated)
				return w.stampCmd(cmd)
			}
		}
		if w.router.CanGoBack() {
			return w.goBack()
		}
		// At root: double-press esc to quit
		if !w.confirmQuit {
			w.confirmQuit = true
			return w.toast.Show("Press Esc again to quit", false)
		}
		w.quitting = true
		return tea.Quit

	case key.Matches(msg, w.keys.Refresh):
		if view := w.router.Current(); view != nil {
			updated, cmd := view.Update(RefreshMsg{})
			w.replaceCurrentView(updated)
			return w.stampCmd(cmd)
		}

	case key.Matches(msg, w.keys.Search):
		// Forward to filterable views first — "/" filters lists locally
		if view := w.router.Current(); view != nil {
			if f, ok := view.(Filterable); ok {
				f.StartFilter()
				w.replaceCurrentView(view)
				return nil
			}
		}
		return w.navigate(ViewSearch, w.session.Scope())

	case key.Matches(msg, w.keys.Palette):
		return w.openPalette()

	case key.Matches(msg, w.keys.AccountSwitch):
		return w.openAccountSwitcher()

	case key.Matches(msg, w.keys.Hey):
		if w.router.CurrentTarget() != ViewHey {
			return w.navigate(ViewHey, w.session.Scope())
		}
		return nil

	case key.Matches(msg, w.keys.MyStuff):
		if w.router.CurrentTarget() != ViewMyStuff {
			return w.navigate(ViewMyStuff, w.session.Scope())
		}
		return nil

	case key.Matches(msg, w.keys.Activity):
		if w.router.CurrentTarget() != ViewActivity {
			return w.navigate(ViewActivity, w.session.Scope())
		}
		return nil

	case key.Matches(msg, w.keys.Open):
		scope := w.session.Scope()
		if fr, ok := w.router.Current().(FocusedRecording); ok {
			fi := fr.FocusedItem()
			if fi.RecordingID != 0 {
				scope.RecordingID = fi.RecordingID
			}
			if fi.ProjectID != 0 {
				scope.ProjectID = fi.ProjectID
			}
			if fi.AccountID != "" {
				scope.AccountID = fi.AccountID
			}
		}
		return w.openFunc(scope)

	case key.Matches(msg, w.keys.Sidebar):
		return w.toggleSidebar()

	case key.Matches(msg, w.keys.SidebarFocus):
		if w.sidebarActive() || w.poolMonitorActive() {
			// If the view has a split pane, route tab to the view instead
			// so it can cycle its internal panes. The sidebar is reachable
			// via ctrl+b (which also cycles focus when already open).
			if !w.sidebarFocused && !w.poolMonitorFocused {
				if view := w.router.Current(); view != nil {
					if sp, ok := view.(SplitPaneFocuser); ok && sp.HasSplitPane() {
						updated, cmd := view.Update(msg)
						w.replaceCurrentView(updated)
						return w.stampCmd(cmd)
					}
				}
			}
			return w.switchSidebarFocus()
		}
		// Fall through to view when no panels are active

	case key.Matches(msg, w.keys.Jump):
		return w.openQuickJump()

	case key.Matches(msg, w.keys.Metrics):
		return w.togglePoolMonitor()

	case key.Matches(msg, w.keys.Bonfire):
		if w.bonfireEnabled() && !w.isBonfireView() {
			return w.navigate(ViewFrontPage, w.session.Scope())
		}
	}

	// Forward to focused panel — panels consume all non-global keys.
	if w.poolMonitorActive() && w.poolMonitorFocused {
		updated, cmd := w.poolMonitor.Update(msg)
		w.poolMonitor = updated
		return w.stampCmd(cmd)
	}
	if w.sidebarActive() && w.sidebarFocused {
		updated, cmd := w.sidebarView.Update(msg)
		w.sidebarView = updated
		return w.stampCmd(cmd)
	}

	// Number keys for breadcrumb jumping (1-9)
	if runes := []rune(msg.Text); len(runes) == 1 {
		r := runes[0]
		if r >= '1' && r <= '9' {
			depth := int(r - '0')
			return w.goToDepth(depth)
		}
	}
	if view := w.router.Current(); view != nil {
		updated, cmd := view.Update(msg)
		w.replaceCurrentView(updated)
		return w.stampCmd(cmd)
	}
	return nil
}

func (w *Workspace) navigate(target ViewTarget, scope Scope) tea.Cmd {
	w.confirmQuit = false
	var cmds []tea.Cmd

	// Release pool monitor keyboard focus (stays visible)
	if cmd := w.clearPoolMonitorFocus(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Blur the outgoing view
	if outgoing := w.router.Current(); outgoing != nil {
		_, cmd := outgoing.Update(BlurMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	}

	// Capture ephemeral origin context, then clear from scope.
	// Origin is meaningful only for the target view, not session state.
	originView := scope.OriginView
	originHint := scope.OriginHint
	scope.OriginView = ""
	scope.OriginHint = ""

	prevAccountID := w.session.Scope().AccountID
	w.session.SetScope(scope)

	// Sync Hub realms to match the target scope. This handles:
	// - Cross-account navigation (Pings → Campfire on different account):
	//   EnsureAccount rotates the account realm + tears down project realm,
	//   and we resolve + update chrome to reflect the new account name.
	// - Forward navigation to non-project views (any view → Hey):
	//   syncProjectRealm tears down the project realm.
	if hub := w.session.Hub(); hub != nil && scope.AccountID != "" {
		hub.EnsureAccount(scope.AccountID)
		// On cross-account hops the cloned scope often carries the old
		// account's name. Resolve the correct name whenever the account
		// actually changed.
		if scope.AccountID != prevAccountID {
			scope.AccountName = "" // clear stale name
			for _, a := range w.session.MultiStore().Accounts() {
				if a.ID == scope.AccountID {
					scope.AccountName = a.Name
					break
				}
			}
			w.session.SetScope(scope)
		}
		if scope.AccountName != "" {
			w.statusBar.SetAccount(scope.AccountName)
		}
	}
	w.syncProjectRealm(scope)

	// Build viewScope from the fully-normalized scope, reattach origin.
	viewScope := scope
	viewScope.OriginView = originView
	viewScope.OriginHint = originHint

	view := w.viewFactory(target, w.session, viewScope)
	w.router.Push(view, viewScope, target)
	w.relayout()
	w.syncAccountBadge(target)
	w.syncChrome()

	// Record navigation quality for observability.
	// Forward navigations start at quality 0 (data not yet loaded).
	w.recordNavigation(view.Title(), 0.0)

	cmds = append(cmds, w.stampCmd(view.Init()), func() tea.Msg { return FocusMsg{} }, chrome.SetTerminalTitle("basecamp - "+view.Title()))
	return tea.Batch(cmds...)
}

func (w *Workspace) goBack() tea.Cmd {
	w.confirmQuit = false
	if !w.router.CanGoBack() {
		return nil
	}
	var cmds []tea.Cmd

	// Release pool monitor keyboard focus (stays visible)
	if cmd := w.clearPoolMonitorFocus(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Blur the outgoing view
	if outgoing := w.router.Current(); outgoing != nil {
		_, cmd := outgoing.Update(BlurMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	}

	w.router.Pop()
	scope := w.router.CurrentScope()
	scope.OriginView = ""
	scope.OriginHint = ""
	w.session.SetScope(scope)
	w.syncProjectRealm(scope)
	w.syncAccountBadge(w.router.CurrentTarget())
	w.syncChrome()
	// Refresh dimensions and focus for the restored view
	w.relayout()
	if view := w.router.Current(); view != nil {
		_, cmd := view.Update(FocusMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
		// Back navigation returns to a view with cached data — quality 1.0.
		w.recordNavigation(view.Title(), 1.0)
		cmds = append(cmds, chrome.SetTerminalTitle("basecamp - "+view.Title()))
	}
	return tea.Batch(cmds...)
}

func (w *Workspace) goToDepth(depth int) tea.Cmd {
	if depth >= w.router.Depth() {
		return nil
	}

	// Release pool monitor keyboard focus (stays visible)
	var cmds []tea.Cmd
	if cmd := w.clearPoolMonitorFocus(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	// Blur the outgoing view
	if outgoing := w.router.Current(); outgoing != nil {
		_, cmd := outgoing.Update(BlurMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	}

	w.router.PopToDepth(depth)
	scope := w.router.CurrentScope()
	scope.OriginView = ""
	scope.OriginHint = ""
	w.session.SetScope(scope)
	w.syncProjectRealm(scope)
	w.syncAccountBadge(w.router.CurrentTarget())
	w.syncChrome()
	// Refresh dimensions and focus for the restored view
	w.relayout()
	if view := w.router.Current(); view != nil {
		_, cmd := view.Update(FocusMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
		w.recordNavigation(view.Title(), 1.0)
		cmds = append(cmds, chrome.SetTerminalTitle("basecamp - "+view.Title()))
	}
	return tea.Batch(cmds...)
}

// toolNameToViewTarget maps dock tool API names to ViewTarget constants.
func toolNameToViewTarget(name string) (ViewTarget, bool) {
	switch name {
	case "todoset":
		return ViewTodos, true
	case "chat":
		return ViewCampfire, true
	case "message_board":
		return ViewMessages, true
	case "kanban_board":
		return ViewCards, true
	case "schedule":
		return ViewSchedule, true
	case "vault":
		return ViewDocsFiles, true
	case "questionnaire":
		return ViewCheckins, true
	case "inbox":
		return ViewForwards, true
	default:
		return 0, false
	}
}

// hubProjects returns the current projects from the Hub's global pool,
// or nil if no data is available yet. Used by quickJump.
func (w *Workspace) hubProjects() []data.ProjectInfo {
	hub := w.session.Hub()
	if hub == nil {
		return nil
	}
	snap := hub.Projects().Get()
	if snap.Usable() {
		return snap.Data
	}
	return nil
}

// syncProjectRealm tears down the project realm when navigation leaves
// project scope. This ensures in-flight project fetches are canceled
// via the realm's context and project pools are released.
func (w *Workspace) syncProjectRealm(scope Scope) {
	hub := w.session.Hub()
	if hub == nil {
		return
	}
	if scope.ProjectID == 0 && hub.Project() != nil {
		hub.LeaveProject()
	}
}

// accountIndex returns the 1-based index of accountID in the discovered
// accounts list, or 0 if not found (used for "All Accounts").
func (w *Workspace) accountIndex(accountID string) int {
	for i, a := range w.accountList {
		if a.ID == accountID {
			return i + 1
		}
	}
	return 0
}

// syncAccountBadge updates the breadcrumb badge based on the current target
// and account context.
func (w *Workspace) syncAccountBadge(target ViewTarget) {
	name := w.session.Scope().AccountName
	multiAccount := len(w.accountList) > 1

	if !multiAccount {
		// Single account (or not yet discovered): plain name badge
		w.breadcrumb.SetAccountBadge(name, false)
		return
	}
	if target.IsGlobal() {
		w.breadcrumb.SetAccountBadge("✱ All Accounts", true)
		return
	}
	// Scoped view: show indexed badge. Fall back to AccountID when name
	// hasn't resolved yet so the badge is never stale/empty.
	label := name
	if label == "" {
		label = w.session.Scope().AccountID
	}
	idx := w.accountIndex(w.session.Scope().AccountID)
	if idx > 0 {
		w.breadcrumb.SetAccountBadgeIndexed(idx, label)
	} else {
		w.breadcrumb.SetAccountBadge(label, false)
	}
}

func (w *Workspace) openPalette() tea.Cmd {
	w.showPalette = true
	w.syncPaletteActions()
	w.palette.SetSize(w.width, w.viewHeight())
	return w.palette.Focus()
}

func (w *Workspace) openQuickJump() tea.Cmd {
	w.showQuickJump = true
	w.quickJump.SetSize(w.width, w.viewHeight())

	scope := w.session.Scope()

	var recentProjects, recentRecordings []recents.Item
	if r := w.session.Recents(); r != nil {
		recentProjects = r.Get(recents.TypeProject, "", "")
		recentRecordings = r.Get(recents.TypeRecording, "", "")
	}

	src := chrome.QuickJumpSource{
		RecentProjects:   recentProjects,
		RecentRecordings: recentRecordings,
		Projects:         w.hubProjects(),
		AccountID:        scope.AccountID,
		NavigateProject: func(projectID int64, accountID string) tea.Cmd {
			return Navigate(ViewDock, Scope{
				AccountID: accountID,
				ProjectID: projectID,
			})
		},
		NavigateRecording: func(recordingID, projectID int64, accountID string) tea.Cmd {
			return Navigate(ViewDetail, Scope{
				AccountID:   accountID,
				ProjectID:   projectID,
				RecordingID: recordingID,
			})
		},
		NavigateTool: func(toolName string, toolID, projectID int64, accountID string) tea.Cmd {
			target, ok := toolNameToViewTarget(toolName)
			if !ok {
				return nil
			}
			return Navigate(target, Scope{
				AccountID: accountID,
				ProjectID: projectID,
				ToolType:  toolName,
				ToolID:    toolID,
			})
		},
	}

	return w.quickJump.Focus(src)
}

func (w *Workspace) openAccountSwitcher() tea.Cmd {
	w.showAccountSwitcher = true
	w.accountSwitcher.SetSize(w.width, w.viewHeight())

	// Build entries from already-discovered accounts
	entries := make([]chrome.AccountEntry, len(w.accountList))
	for i, a := range w.accountList {
		entries[i] = chrome.AccountEntry{ID: a.ID, Name: a.Name}
	}
	return w.accountSwitcher.Focus(entries)
}

func (w *Workspace) toggleSidebar() tea.Cmd {
	if w.showSidebar && w.sidebarView != nil {
		// Blur outgoing sidebar
		w.sidebarView.Update(BlurMsg{})

		// Advance to next panel, or close if at end
		w.sidebarIndex++
		if w.sidebarIndex >= len(w.sidebarTargets) {
			// Close
			w.sidebarView = nil
			w.showSidebar = false
			w.sidebarFocused = false
			w.sidebarIndex = -1
			w.relayout()
			// Refocus main view
			if view := w.router.Current(); view != nil {
				updated, cmd := view.Update(FocusMsg{})
				w.replaceCurrentView(updated)
				return w.stampCmd(cmd)
			}
			return nil
		}
		// Switch to next panel — reset focus to main
		w.sidebarFocused = false
		return w.openSidebarPanel(w.sidebarTargets[w.sidebarIndex])
	}
	// Open from closed
	w.sidebarIndex = 0
	w.showSidebar = true
	w.sidebarFocused = false
	blurCmd := w.clearPoolMonitorFocus()
	return tea.Batch(blurCmd, w.openSidebarPanel(w.sidebarTargets[0]))
}

func (w *Workspace) openSidebarPanel(target ViewTarget) tea.Cmd {
	scope := w.session.Scope()
	w.sidebarView = w.viewFactory(target, w.session, scope)
	blurCmd := w.clearPoolMonitorFocus()
	w.relayout()
	// Init new sidebar; refocus main view
	cmds := []tea.Cmd{blurCmd, w.stampCmd(w.sidebarView.Init())}
	if view := w.router.Current(); view != nil {
		updated, cmd := view.Update(FocusMsg{})
		w.replaceCurrentView(updated)
		cmds = append(cmds, w.stampCmd(cmd))
	}
	return tea.Batch(cmds...)
}

func (w *Workspace) togglePoolMonitor() tea.Cmd {
	if w.showPoolMonitor {
		if w.poolMonitorFocused {
			// Focused → close
			w.showPoolMonitor = false
			w.poolMonitorFocused = false
			blurred, blurCmd := w.poolMonitor.Update(BlurMsg{})
			w.poolMonitor = blurred
			w.relayout()
			if view := w.router.Current(); view != nil {
				updated, cmd := view.Update(FocusMsg{})
				w.replaceCurrentView(updated)
				return tea.Batch(w.stampCmd(blurCmd), w.stampCmd(cmd))
			}
			return w.stampCmd(blurCmd)
		}
		// Open but unfocused → focus it (only if renderable)
		if !w.poolMonitorActive() {
			return nil
		}
		w.poolMonitorFocused = true
		var cmds []tea.Cmd
		if w.sidebarFocused {
			w.sidebarFocused = false
			sUpdated, _ := w.sidebarView.Update(BlurMsg{})
			w.sidebarView = sUpdated
		} else if view := w.router.Current(); view != nil {
			updated, cmd := view.Update(BlurMsg{})
			w.replaceCurrentView(updated)
			if cmd != nil {
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
		focused, cmd := w.poolMonitor.Update(FocusMsg{})
		w.poolMonitor = focused
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
		return tea.Batch(cmds...)
	}
	// Closed → open (unfocused)
	if w.poolMonitorFactory != nil && w.poolMonitor == nil {
		w.poolMonitor = w.poolMonitorFactory()
	}
	if w.poolMonitor == nil {
		return nil
	}
	w.showPoolMonitor = true
	w.poolMonitorFocused = false
	w.relayout()
	return w.stampCmd(w.poolMonitor.Init())
}

// switchSidebarFocus cycles tab focus: main → sidebar → pool monitor → main.
// Panels that aren't active are skipped.
func (w *Workspace) switchSidebarFocus() tea.Cmd {
	type panel int
	const (
		panelMain panel = iota
		panelSidebar
		panelMonitor
	)
	current := panelMain
	if w.sidebarFocused {
		current = panelSidebar
	} else if w.poolMonitorFocused {
		current = panelMonitor
	}

	// Build cycle of available panels
	order := []panel{panelMain}
	if w.sidebarActive() {
		order = append(order, panelSidebar)
	}
	if w.poolMonitorActive() {
		order = append(order, panelMonitor)
	}

	// Find current in cycle and advance
	next := panelMain
	for i, p := range order {
		if p == current {
			next = order[(i+1)%len(order)]
			break
		}
	}

	// Blur current, focus next
	var cmds []tea.Cmd
	switch current {
	case panelMain:
		if view := w.router.Current(); view != nil {
			_, cmd := view.Update(BlurMsg{})
			if cmd != nil {
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
	case panelSidebar:
		w.sidebarFocused = false
		updated, cmd := w.sidebarView.Update(BlurMsg{})
		w.sidebarView = updated
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	case panelMonitor:
		w.poolMonitorFocused = false
		if w.poolMonitor != nil {
			updated, cmd := w.poolMonitor.Update(BlurMsg{})
			w.poolMonitor = updated
			if cmd != nil {
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
	}

	switch next {
	case panelMain:
		if view := w.router.Current(); view != nil {
			_, cmd := view.Update(FocusMsg{})
			if cmd != nil {
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
	case panelSidebar:
		w.sidebarFocused = true
		updated, cmd := w.sidebarView.Update(FocusMsg{})
		w.sidebarView = updated
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	case panelMonitor:
		w.poolMonitorFocused = true
		focused, cmd := w.poolMonitor.Update(FocusMsg{})
		w.poolMonitor = focused
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	}

	return tea.Batch(cmds...)
}

// clearPoolMonitorFocus blurs the pool monitor if focused, returning any
// cmd produced by the blur so callers can propagate it.
func (w *Workspace) clearPoolMonitorFocus() tea.Cmd {
	if w.poolMonitorFocused {
		w.poolMonitorFocused = false
		if w.poolMonitor != nil {
			updated, cmd := w.poolMonitor.Update(BlurMsg{})
			w.poolMonitor = updated
			return w.stampCmd(cmd)
		}
	}
	return nil
}

func (w *Workspace) switchAccount(accountID, accountName string) tea.Cmd {
	// Release pool monitor keyboard focus (stays visible)
	blurCmd := w.clearPoolMonitorFocus()

	// Update session scope with new account
	scope := Scope{
		AccountID:   accountID,
		AccountName: accountName,
	}
	w.session.SetScope(scope)

	// Cancel in-flight operations from the old account context.
	w.session.ResetContext()

	// Rotate Hub realms to the new account.
	w.session.Hub().SwitchAccount(accountID)

	// Update status bar
	w.statusBar.SetAccount(accountName)

	// Reset navigation and push fresh home dashboard
	w.router.Reset()
	view := w.viewFactory(ViewHome, w.session, scope)
	w.router.Push(view, scope, ViewHome)
	w.syncAccountBadge(ViewHome)
	w.syncChrome()
	w.relayout()

	return tea.Batch(blurCmd, w.stampCmd(view.Init()), func() tea.Msg { return FocusMsg{} }, chrome.SetTerminalTitle("basecamp"))
}

func (w *Workspace) syncPaletteActions() {
	scope := w.session.Scope()
	actions := w.registry.ForScope(scope)

	names := make([]string, 0, len(actions))
	descriptions := make([]string, 0, len(actions))
	categories := make([]string, 0, len(actions))
	executors := make([]func() tea.Cmd, 0, len(actions))

	for _, a := range actions {
		if a.Experimental != "" && !w.isExperimentalEnabled(a.Experimental) {
			continue
		}
		names = append(names, a.Name)
		descriptions = append(descriptions, a.Description)
		categories = append(categories, a.Category)
		sess := w.session
		exec := a.Execute
		executors = append(executors, func() tea.Cmd {
			return exec(sess)
		})
	}
	w.palette.SetActions(names, descriptions, categories, executors)
}

// stampCmd wraps a view-returned Cmd with the current session epoch.
// When the Cmd's result arrives, Workspace.Update checks the epoch: if it no
// longer matches (an account switch occurred), the result is silently dropped
// instead of being forwarded to the now-unrelated current view.
func (w *Workspace) stampCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return stampWithEpoch(w.session.Epoch(), cmd)
}

// stampWithEpoch wraps a tea.Cmd so its result carries an epoch tag.
// BatchMsg results are handled recursively — each inner Cmd is individually
// stamped so that batch members are also epoch-guarded.
func stampWithEpoch(epoch uint64, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if msg == nil {
			return nil
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			stamped := make(tea.BatchMsg, len(batch))
			for i, c := range batch {
				stamped[i] = stampWithEpoch(epoch, c)
			}
			return stamped
		}
		return EpochMsg{Epoch: epoch, Inner: msg}
	}
}

func (w *Workspace) replaceCurrentView(updated View) {
	if len(w.router.stack) > 0 {
		w.router.stack[len(w.router.stack)-1].view = updated
	}
	w.statusBar.SetKeyHints(updated.ShortHelp())
}

// recordNavigation logs a navigation event for Apdex tracking.
// quality: 1.0 = cached/fresh, 0.5 = stale, 0.0 = empty/loading.
func (w *Workspace) recordNavigation(viewTitle string, quality float64) {
	if hub := w.session.Hub(); hub != nil {
		hub.Metrics().RecordNavigation(data.NavigationEvent{
			Timestamp: time.Now(),
			ViewTitle: viewTitle,
			Quality:   quality,
		})
	}
}

func (w *Workspace) syncChrome() {
	w.breadcrumb.SetCrumbs(w.router.Breadcrumbs())
	w.help.SetGlobalKeys(w.filterFullHelp())

	globalHints := w.keys.ShortHelp()
	if hide := w.hiddenBindingKeys(); len(hide) > 0 {
		filtered := make([]key.Binding, 0, len(globalHints))
		for _, b := range globalHints {
			if k := b.Keys(); len(k) > 0 && hide[k[0]] {
				continue
			}
			filtered = append(filtered, b)
		}
		globalHints = filtered
	}
	w.statusBar.SetGlobalHints(globalHints)
	if view := w.router.Current(); view != nil {
		w.statusBar.SetKeyHints(view.ShortHelp())
		w.help.SetViewTitle(view.Title())
		w.help.SetViewKeys(view.FullHelp())
	}
}

// sidebarMinWidth is the minimum terminal width for showing the sidebar.
const sidebarMinWidth = 100

func (w *Workspace) relayout() {
	w.breadcrumb.SetWidth(w.width)
	w.statusBar.SetWidth(w.width)
	w.toast.SetWidth(w.width)
	w.help.SetSize(w.width, w.viewHeight())
	w.palette.SetSize(w.width, w.viewHeight())
	w.accountSwitcher.SetSize(w.width, w.viewHeight())
	w.quickJump.SetSize(w.width, w.viewHeight())
	w.boostPicker.SetSize(w.width, w.height)

	contentWidth := w.width
	if w.poolMonitorActive() {
		contentWidth -= poolMonitorWidth + 1 // +1 for divider
		w.poolMonitor.SetSize(poolMonitorWidth, w.viewHeight())
	}

	if w.sidebarActive() {
		sidebarW := int(float64(contentWidth) * w.sidebarRatio)
		mainW := contentWidth - sidebarW - 1 // -1 for divider
		w.sidebarView.SetSize(sidebarW, w.viewHeight())
		if view := w.router.Current(); view != nil {
			view.SetSize(mainW, w.viewHeight())
		}
	} else if view := w.router.Current(); view != nil {
		view.SetSize(contentWidth, w.viewHeight())
	}
}

// filterFullHelp returns FullHelp with context-dependent bindings removed.
func (w *Workspace) filterFullHelp() [][]key.Binding {
	groups := w.keys.FullHelp()
	hide := w.hiddenBindingKeys()
	if len(hide) == 0 {
		return groups
	}
	out := make([][]key.Binding, 0, len(groups))
	for _, group := range groups {
		var filtered []key.Binding
		for _, b := range group {
			if k := b.Keys(); len(k) > 0 {
				if hide[k[0]] {
					continue
				}
			}
			filtered = append(filtered, b)
		}
		if len(filtered) > 0 {
			out = append(out, filtered)
		}
	}
	return out
}

// hiddenBindingKeys returns key strings that should be hidden from help.
func (w *Workspace) hiddenBindingKeys() map[string]bool {
	m := make(map[string]bool)
	if len(w.accountList) <= 1 {
		if k := w.keys.AccountSwitch.Keys(); len(k) > 0 {
			m[k[0]] = true
		}
	}
	if !w.bonfireEnabled() {
		if k := w.keys.Bonfire.Keys(); len(k) > 0 {
			m[k[0]] = true
		}
	}
	return m
}

// isExperimentalEnabled returns true when the named experimental feature is on.
func (w *Workspace) isExperimentalEnabled(name string) bool {
	if app := w.session.App(); app != nil {
		return app.Config.IsExperimental(name)
	}
	return false
}

// bonfireEnabled returns true when the experimental bonfire feature is on.
func (w *Workspace) bonfireEnabled() bool {
	return w.isExperimentalEnabled("bonfire")
}

// defaultSidebarTargets returns sidebar targets, including bonfire only when enabled.
func defaultSidebarTargets(session *Session) []ViewTarget {
	targets := []ViewTarget{ViewActivity, ViewHome}
	if app := session.App(); app != nil && app.Config.IsExperimental("bonfire") {
		targets = append([]ViewTarget{ViewBonfireSidebar}, targets...)
	}
	return targets
}

// isBonfireView returns true when the current view is a campfire-related target.
// Used to prevent ctrl+g from pushing duplicate nav entries.
func (w *Workspace) isBonfireView() bool {
	switch w.router.CurrentTarget() {
	case ViewBonfire, ViewFrontPage, ViewBonfireSidebar, ViewCampfire:
		return true
	default:
		return false
	}
}

// sidebarActive returns true when the left sidebar should be rendered.
func (w *Workspace) sidebarActive() bool {
	return w.showSidebar && w.sidebarView != nil && w.width >= sidebarMinWidth
}

// poolMonitorActive returns true when the pool monitor right sidebar should be rendered.
// Checks that the main content area retains at least minMainWidth after allocating
// space for both the left sidebar (if active) and the pool monitor + its divider.
// Uses the same geometry as relayout: sidebar ratio applies to contentWidth
// (total width minus monitor and its divider).
func (w *Workspace) poolMonitorActive() bool {
	if !w.showPoolMonitor || w.poolMonitor == nil {
		return false
	}
	contentWidth := w.width - poolMonitorWidth - 1 // -1 for right divider
	mainSpace := contentWidth
	if w.sidebarActive() {
		sidebarW := int(float64(contentWidth) * w.sidebarRatio)
		mainSpace = contentWidth - sidebarW - 1 // -1 for left divider
	}
	return mainSpace >= minMainWidth
}

func (w *Workspace) viewHeight() int {
	h := w.height - w.chromeHeight()
	if h < 1 {
		h = 1
	}
	return h
}

// View implements tea.Model.
func (w *Workspace) View() tea.View {
	if w.quitting {
		return tea.NewView("")
	}

	var sections []string

	// Breadcrumb
	sections = append(sections, w.breadcrumb.View())

	// Divider
	theme := w.styles.Theme()
	divider := lipgloss.NewStyle().
		Width(w.width).
		Foreground(theme.Border).
		Render(strings.Repeat("─", max(1, w.width)))
	sections = append(sections, divider)

	// Main view
	if w.showAccountSwitcher {
		sections = append(sections, w.accountSwitcher.View())
	} else if w.showQuickJump {
		sections = append(sections, w.quickJump.View())
	} else if w.showPalette {
		sections = append(sections, w.palette.View())
	} else if w.showHelp {
		sections = append(sections, w.help.View())
	} else {
		vDividerStr := strings.TrimRight(strings.Repeat("│\n", w.viewHeight()), "\n")
		vDivider := lipgloss.NewStyle().
			Foreground(theme.Border).
			Height(w.viewHeight()).
			Render(vDividerStr)

		var content string
		if w.sidebarActive() {
			mainContent := ""
			if view := w.router.Current(); view != nil {
				mainContent = view.View()
			}
			content = lipgloss.JoinHorizontal(lipgloss.Top,
				w.sidebarView.View(), vDivider, mainContent)
		} else if view := w.router.Current(); view != nil {
			content = view.View()
		}

		if w.poolMonitorActive() {
			content = lipgloss.JoinHorizontal(lipgloss.Top,
				content, vDivider, w.poolMonitor.View())
		}
		sections = append(sections, content)
	}

	// Status bar
	sections = append(sections, w.statusBar.View())

	ui := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Toast overlay: replace the penultimate line (above status bar) rather
	// than adding a layout section, so the main content height stays constant.
	if w.toast.Visible() {
		toastStr := w.toast.View()
		lines := strings.Split(ui, "\n")
		if len(lines) >= 2 {
			lines[len(lines)-2] = toastStr
			ui = strings.Join(lines, "\n")
		}
	}

	if w.pickingBoost {
		pickerView := w.boostPicker.View()
		ui = lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, pickerView)
	}

	v := tea.NewView(ui)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = w.windowTitle
	v.ReportFocus = true
	return v
}

// isAuthError returns true if the error indicates an expired or invalid auth token.
// Checks the typed SDK error code first, falling back to string matching for
// errors that don't go through the SDK error path.
// humanizeError converts raw Go error strings into user-friendly messages.
func humanizeError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "no such host"),
		strings.Contains(s, "dial tcp"),
		strings.Contains(s, "connection refused"):
		return "Could not connect to Basecamp"
	case strings.Contains(s, "timeout"),
		strings.Contains(s, "deadline exceeded"):
		return "Request timed out"
	case strings.Contains(s, "EOF"),
		strings.Contains(s, "connection reset"):
		return "Connection interrupted"
	case strings.Contains(s, "403"),
		strings.Contains(s, "forbidden"):
		return "Access denied"
	case strings.Contains(s, "404"),
		strings.Contains(s, "not found"):
		return "Not found"
	case strings.Contains(s, "500"),
		strings.Contains(s, "502"),
		strings.Contains(s, "503"):
		return "Basecamp is temporarily unavailable"
	default:
		if utf8.RuneCountInString(s) > 80 {
			return string([]rune(s)[:79]) + "…"
		}
		return s
	}
}

func isAuthError(err error) bool {
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) && sdkErr.Code == basecamp.CodeAuth {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "401") || strings.Contains(s, "Unauthorized") || strings.Contains(s, "unauthorized")
}

func (w *Workspace) createBoost(target BoostTarget, emoji string) tea.Cmd {
	return func() tea.Msg {
		ctx := w.session.Hub().ProjectContext()
		_, err := w.session.Hub().CreateBoost(ctx, target.AccountID, target.ProjectID, target.RecordingID, emoji)
		if err != nil {
			return ErrorMsg{Err: err, Context: "creating boost"}
		}
		// Refetch boosts or timeline
		return tea.BatchMsg{
			func() tea.Msg { return BoostCreatedMsg{Target: target, Emoji: emoji} },
			func() tea.Msg { return StatusMsg{Text: "Boosted!"} },
		}
	}
}

// CloseWatcher shuts down the theme file watcher, if running.
// Safe to call multiple times.
func (w *Workspace) CloseWatcher() {
	if w.themeWatcher != nil {
		w.themeWatcher.Close()
		w.themeWatcher = nil
	}
}

// startThemeWatcher sets up fsnotify on the resolved theme file's parent
// directory and returns a Cmd that blocks until the file changes.
func (w *Workspace) startThemeWatcher() tea.Cmd {
	path := tui.ThemeFilePath()
	if path == "" {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	if err := watcher.Add(filepath.Dir(resolved)); err != nil {
		watcher.Close()
		return nil
	}
	w.themeWatcher = watcher
	return waitForThemeChange(watcher, resolved)
}

// waitForThemeChange blocks on fsnotify events and emits ThemeChangedMsg
// when the target file is written or created.
func waitForThemeChange(watcher *fsnotify.Watcher, target string) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				if event.Name == target && (event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename)) {
					return ThemeChangedMsg{}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
			}
		}
	}
}
