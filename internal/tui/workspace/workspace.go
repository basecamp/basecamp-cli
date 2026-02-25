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

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/chrome"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// chromeHeight is the vertical space reserved for breadcrumb + status bar + toast.
const chromeHeight = 3

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

	// Metrics panel
	metricsPanel chrome.MetricsPanel
	showMetrics  bool

	// State
	showHelp            bool
	showPalette         bool
	showAccountSwitcher bool
	showQuickJump       bool
	quitting            bool
	confirmQuit         bool

	// ViewFactory builds views from targets — set by the command that creates the workspace.
	viewFactory ViewFactory
	openFunc    func(Scope) tea.Cmd

	// createBoostFunc is the function called to create a boost. Defaults to
	// createBoost; tests can replace it with a spy.
	createBoostFunc func(BoostTarget, string) tea.Cmd

	width, height int
}

// ViewFactory creates views for navigation targets.
type ViewFactory func(target ViewTarget, session *Session, scope Scope) View

// New creates a new Workspace model.
func New(session *Session, factory ViewFactory) *Workspace {
	styles := session.Styles()
	registry := DefaultActions()

	keys := DefaultGlobalKeyMap()
	if configDir, err := os.UserConfigDir(); err == nil {
		overrides, err := LoadKeyOverrides(filepath.Join(configDir, "basecamp", "keybindings.json"))
		if err != nil {
			log.Printf("keybindings: %v", err)
		}
		if len(overrides) > 0 {
			ApplyOverrides(&keys, overrides)
		}
	}

	w := &Workspace{
		session:         session,
		router:          NewRouter(),
		styles:          styles,
		keys:            keys,
		registry:        registry,
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles),
		quickJump:       chrome.NewQuickJump(styles),
		boostPicker:     NewBoostPicker(styles),
		viewFactory:     factory,
		openFunc:        openInBrowser,
		sidebarTargets:  []ViewTarget{ViewActivity, ViewHome},
		sidebarIndex:    -1,
		sidebarRatio:    0.30,
	}
	w.createBoostFunc = w.createBoost

	// Metrics panel reads live stats from the Hub's metrics collector.
	if hub := session.Hub(); hub != nil {
		m := hub.Metrics()
		w.metricsPanel = chrome.NewMetricsPanel(styles, m.PoolStatsList, m.Apdex)
	}

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

	cmds := []tea.Cmd{w.stampCmd(view.Init()), chrome.SetTerminalTitle("basecamp")}

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

// Update implements tea.Model.
func (w *Workspace) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		w.height = msg.Height
		w.relayout()
		return w, nil

	case tea.KeyMsg:
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
		// Refresh Home/Projects after discovery completes. This handles:
		// - Multi-account: views switch to cross-account fan-out mode.
		// - Single-account: identity is now available for identity-dependent
		//   pools (Assignments), replacing bootstrap-empty data.
		if view := w.router.Current(); view != nil {
			title := view.Title()
			if title == "Home" || title == "Projects" {
				updated, cmd := view.Update(RefreshMsg{})
				w.replaceCurrentView(updated)
				return w, w.stampCmd(cmd)
			}
		}
		return w, nil

	case BoostSelectedMsg:
		w.pickingBoost = false
		w.boostPicker.Blur()
		return w, w.createBoostFunc(w.boostTarget, msg.Emoji)

	case OpenBoostPickerMsg:
		w.pickingBoost = true
		w.boostTarget = msg.Target
		w.boostPicker.Focus()
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
		// Forward to sidebar if active
		var sidebarCmd tea.Cmd
		if w.sidebarActive() {
			updated, sc := w.sidebarView.Update(msg)
			if v, ok := updated.(View); ok {
				w.sidebarView = v
			}
			sidebarCmd = w.stampCmd(sc)
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

	// Forward PollMsg to sidebar alongside the main view
	// (PoolUpdatedMsg is handled by the explicit case above)
	var sidebarCmd tea.Cmd
	if w.sidebarActive() {
		if _, ok := msg.(data.PollMsg); ok {
			updated, sc := w.sidebarView.Update(msg)
			if v, ok := updated.(View); ok {
				w.sidebarView = v
			}
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

func (w *Workspace) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Help overlay consumes all keys when active
	if w.pickingBoost {
		switch msg.Type {
		case tea.KeyEsc:
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
			return w.navigate(ViewHey, w.session.Scope())
		case key.Matches(msg, w.keys.MyStuff):
			return w.navigate(ViewMyStuff, w.session.Scope())
		case key.Matches(msg, w.keys.Activity):
			return w.navigate(ViewActivity, w.session.Scope())
		case key.Matches(msg, w.keys.Sidebar):
			return w.toggleSidebar()
		case key.Matches(msg, w.keys.Jump):
			return w.openQuickJump()
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
		return w.navigate(ViewHey, w.session.Scope())

	case key.Matches(msg, w.keys.MyStuff):
		return w.navigate(ViewMyStuff, w.session.Scope())

	case key.Matches(msg, w.keys.Activity):
		return w.navigate(ViewActivity, w.session.Scope())

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
		if w.sidebarActive() {
			// If the view has a split pane, route tab to the view instead
			// so it can cycle its internal panes. The sidebar is reachable
			// via ctrl+b (which also cycles focus when already open).
			if view := w.router.Current(); view != nil {
				if sp, ok := view.(SplitPaneFocuser); ok && sp.HasSplitPane() {
					updated, cmd := view.Update(msg)
					w.replaceCurrentView(updated)
					return w.stampCmd(cmd)
				}
			}
			return w.switchSidebarFocus()
		}
		// Fall through to view when sidebar is inactive

	case key.Matches(msg, w.keys.Jump):
		return w.openQuickJump()

	case key.Matches(msg, w.keys.Metrics):
		w.showMetrics = !w.showMetrics
		w.relayout()
		return nil
	}

	// Number keys for breadcrumb jumping (1-9)
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		r := msg.Runes[0]
		if r >= '1' && r <= '9' {
			depth := int(r - '0')
			return w.goToDepth(depth)
		}
	}

	// Forward to focused panel
	if w.sidebarActive() && w.sidebarFocused {
		updated, cmd := w.sidebarView.Update(msg)
		if v, ok := updated.(View); ok {
			w.sidebarView = v
		}
		return w.stampCmd(cmd)
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
	view.SetSize(w.width, w.viewHeight())
	w.router.Push(view, viewScope, target)
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
	if view := w.router.Current(); view != nil {
		view.SetSize(w.width, w.viewHeight())
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
	var cmds []tea.Cmd
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
	if view := w.router.Current(); view != nil {
		view.SetSize(w.width, w.viewHeight())
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
	return w.openSidebarPanel(w.sidebarTargets[0])
}

func (w *Workspace) openSidebarPanel(target ViewTarget) tea.Cmd {
	scope := w.session.Scope()
	w.sidebarView = w.viewFactory(target, w.session, scope)
	w.relayout()
	// Init new sidebar; refocus main view
	cmds := []tea.Cmd{w.stampCmd(w.sidebarView.Init())}
	if view := w.router.Current(); view != nil {
		updated, cmd := view.Update(FocusMsg{})
		w.replaceCurrentView(updated)
		cmds = append(cmds, w.stampCmd(cmd))
	}
	return tea.Batch(cmds...)
}

func (w *Workspace) switchSidebarFocus() tea.Cmd {
	if !w.sidebarActive() {
		return nil
	}
	var cmds []tea.Cmd
	w.sidebarFocused = !w.sidebarFocused
	if w.sidebarFocused {
		if view := w.router.Current(); view != nil {
			_, cmd := view.Update(BlurMsg{})
			if cmd != nil {
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
		_, cmd := w.sidebarView.Update(FocusMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
	} else {
		_, cmd := w.sidebarView.Update(BlurMsg{})
		if cmd != nil {
			cmds = append(cmds, w.stampCmd(cmd))
		}
		if view := w.router.Current(); view != nil {
			_, cmd := view.Update(FocusMsg{})
			if cmd != nil {
				cmds = append(cmds, w.stampCmd(cmd))
			}
		}
	}
	return tea.Batch(cmds...)
}

func (w *Workspace) switchAccount(accountID, accountName string) tea.Cmd {
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
	view.SetSize(w.width, w.viewHeight())
	w.router.Push(view, scope, ViewHome)
	w.syncAccountBadge(ViewHome)
	w.syncChrome()

	return tea.Batch(w.stampCmd(view.Init()), func() tea.Msg { return FocusMsg{} }, chrome.SetTerminalTitle("basecamp"))
}

func (w *Workspace) syncPaletteActions() {
	scope := w.session.Scope()
	actions := w.registry.ForScope(scope)

	names := make([]string, len(actions))
	descriptions := make([]string, len(actions))
	categories := make([]string, len(actions))
	executors := make([]func() tea.Cmd, len(actions))

	for i, a := range actions {
		names[i] = a.Name
		descriptions[i] = a.Description
		categories[i] = a.Category
		// Capture session for the closure
		sess := w.session
		exec := a.Execute
		executors[i] = func() tea.Cmd {
			return exec(sess)
		}
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

func (w *Workspace) replaceCurrentView(updated tea.Model) {
	if v, ok := updated.(View); ok {
		if len(w.router.stack) > 0 {
			w.router.stack[len(w.router.stack)-1].view = v
		}
		// Refresh key hints — view mode may have changed (e.g., cards move mode)
		w.statusBar.SetKeyHints(v.ShortHelp())
	}
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
	w.help.SetGlobalKeys(w.keys.FullHelp())

	globalHints := w.keys.ShortHelp()
	if len(w.accountList) <= 1 {
		// Remove AccountSwitch hint when only one account
		filtered := make([]key.Binding, 0, len(globalHints))
		for _, b := range globalHints {
			if keys := b.Keys(); len(keys) > 0 && keys[0] == w.keys.AccountSwitch.Keys()[0] {
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
	w.metricsPanel.SetWidth(w.width)
	w.help.SetSize(w.width, w.viewHeight())
	w.palette.SetSize(w.width, w.viewHeight())
	w.accountSwitcher.SetSize(w.width, w.viewHeight())
	w.quickJump.SetSize(w.width, w.viewHeight())
	w.boostPicker.SetSize(w.width, w.height)

	if w.sidebarActive() {
		sidebarW := int(float64(w.width) * w.sidebarRatio)
		mainW := w.width - sidebarW - 1 // -1 for divider
		w.sidebarView.SetSize(sidebarW, w.viewHeight())
		if view := w.router.Current(); view != nil {
			view.SetSize(mainW, w.viewHeight())
		}
	} else if view := w.router.Current(); view != nil {
		view.SetSize(w.width, w.viewHeight())
	}
}

// sidebarActive returns true when the sidebar should be rendered.
func (w *Workspace) sidebarActive() bool {
	return w.showSidebar && w.sidebarView != nil && w.width >= sidebarMinWidth
}

func (w *Workspace) viewHeight() int {
	h := w.height - chromeHeight
	if w.showMetrics {
		h -= chrome.MetricsPanelHeight
	}
	if h < 1 {
		h = 1
	}
	return h
}

// View implements tea.Model.
func (w *Workspace) View() string {
	if w.quitting {
		return ""
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
	} else if w.sidebarActive() {
		vDivider := lipgloss.NewStyle().
			Foreground(theme.Border).
			Height(w.viewHeight()).
			Render(strings.TrimRight(strings.Repeat("│\n", w.viewHeight()), "\n"))
		mainContent := ""
		if view := w.router.Current(); view != nil {
			mainContent = view.View()
		}
		sections = append(sections,
			lipgloss.JoinHorizontal(lipgloss.Top,
				w.sidebarView.View(), vDivider, mainContent))
	} else if view := w.router.Current(); view != nil {
		sections = append(sections, view.View())
	}

	// Metrics panel (above status bar when active)
	if w.showMetrics {
		sections = append(sections, w.metricsPanel.View())
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

	return ui
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
