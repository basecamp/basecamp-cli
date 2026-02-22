package workspace

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/chrome"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// testView satisfies View, InputCapturer, and ModalActive for workspace tests.
type testView struct {
	title       string
	msgs        []tea.Msg
	inputActive bool
	modalActive bool
}

func (v *testView) Init() tea.Cmd { return nil }
func (v *testView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	v.msgs = append(v.msgs, msg)
	return v, nil
}
func (v *testView) View() string              { return v.title }
func (v *testView) Title() string             { return v.title }
func (v *testView) ShortHelp() []key.Binding  { return nil }
func (v *testView) FullHelp() [][]key.Binding { return nil }
func (v *testView) SetSize(int, int)          {}
func (v *testView) InputActive() bool         { return v.inputActive }
func (v *testView) IsModal() bool             { return v.modalActive }

// testSession returns a minimal Session suitable for unit tests.
func testSession() *Session {
	return &Session{
		styles: tui.NewStyles(),
	}
}

// testWorkspace builds a Workspace wired for testing, bypassing New() and its
// heavy SDK/auth dependencies. The returned viewLog captures every view the
// factory creates so tests can inspect messages forwarded to those views.
func testWorkspace() (w *Workspace, viewLog *[]*testView) {
	styles := tui.NewStyles()
	session := testSession()
	log := make([]*testView, 0)

	factory := func(target ViewTarget, _ *Session, scope Scope) View {
		v := &testView{title: targetName(target)}
		log = append(log, v)
		return v
	}

	w = &Workspace{
		session:         session,
		router:          NewRouter(),
		styles:          styles,
		keys:            DefaultGlobalKeyMap(),
		registry:        DefaultActions(),
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles),
		viewFactory:     factory,
		sidebarTargets:  []ViewTarget{ViewActivity, ViewHome},
		sidebarIndex:    -1,
		width:           120,
		height:          40,
	}

	return w, &log
}

func targetName(t ViewTarget) string {
	names := map[ViewTarget]string{
		ViewProjects:    "Projects",
		ViewDock:        "Dock",
		ViewTodos:       "Todos",
		ViewCampfire:    "Campfire",
		ViewHey:         "Hey!",
		ViewCards:       "Cards",
		ViewMessages:    "Messages",
		ViewSearch:      "Search",
		ViewMyStuff:     "My Stuff",
		ViewPulse:       "Pulse",
		ViewAssignments: "Assignments",
		ViewPings:       "Pings",
		ViewActivity:    "Activity",
		ViewTimeline:    "Project Activity",
		ViewHome:        "Home",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return "Unknown"
}

// pushTestView is a helper that pushes a named testView onto the workspace router.
func pushTestView(w *Workspace, title string) *testView {
	v := &testView{title: title}
	w.router.Push(v, Scope{}, 0)
	w.syncChrome()
	return v
}

func keyMsg(k string) tea.KeyMsg {
	// Translate common key names to tea.KeyMsg
	switch k {
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	// Single rune keys
	if len(k) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// --- Tests ---

func TestWorkspace_QuitKey(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")

	cmd := w.handleKey(keyMsg("q"))
	require.NotNil(t, cmd, "q should produce a command")

	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "q should produce tea.QuitMsg")
	assert.True(t, w.quitting)
}

func TestWorkspace_BackNavigation(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")
	pushTestView(w, "Child")

	assert.Equal(t, 2, w.router.Depth())
	assert.Equal(t, "Child", w.router.Current().Title())

	w.handleKey(keyMsg("esc"))

	assert.Equal(t, 1, w.router.Depth())
	assert.Equal(t, "Root", w.router.Current().Title())
}

func TestWorkspace_BackAtRootQuits(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")

	cmd := w.handleKey(keyMsg("esc"))
	require.NotNil(t, cmd)

	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "Esc at root should quit")
	assert.True(t, w.quitting)
}

func TestWorkspace_BreadcrumbJump(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")
	pushTestView(w, "Level 2")
	pushTestView(w, "Level 3")

	assert.Equal(t, 3, w.router.Depth())

	// Press "2" to jump to depth 2
	w.handleKey(keyMsg("2"))

	assert.Equal(t, 2, w.router.Depth())
	assert.Equal(t, "Level 2", w.router.Current().Title())
}

func TestWorkspace_InputCaptureSkipsGlobals(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Root")
	v.inputActive = true

	// "q" should NOT quit when input is active
	cmd := w.handleKey(keyMsg("q"))

	assert.False(t, w.quitting, "q should not quit during input capture")
	assert.Nil(t, cmd, "forwarded to view which returns nil cmd")

	// Verify the view received the key
	require.NotEmpty(t, v.msgs, "view should have received the key")
	_, isKey := v.msgs[len(v.msgs)-1].(tea.KeyMsg)
	assert.True(t, isKey, "view should receive the key message")
}

func TestWorkspace_ModalEscGoesToView(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Root")
	pushTestView(w, "Child")

	// Make the child modal
	child := w.router.Current().(*testView)
	child.modalActive = true

	// Esc should go to the view, not trigger back navigation
	w.handleKey(keyMsg("esc"))

	// Stack depth should remain 2 (Esc was forwarded to view, not consumed as back)
	assert.Equal(t, 2, w.router.Depth(), "modal Esc should not pop the stack")
	_ = v // root should not be revealed

	// The child should have received the esc key
	require.NotEmpty(t, child.msgs)
	received := child.msgs[len(child.msgs)-1]
	km, isKey := received.(tea.KeyMsg)
	assert.True(t, isKey)
	assert.Equal(t, tea.KeyEscape, km.Type)
}

func TestWorkspace_CtrlCAlwaysQuits(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Root")
	v.inputActive = true

	cmd := w.handleKey(keyMsg("ctrl+c"))
	require.NotNil(t, cmd)

	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "ctrl+c should quit even during input capture")
	assert.True(t, w.quitting)
}

func TestWorkspace_NavigateMsg(t *testing.T) {
	w, viewLog := testWorkspace()
	pushTestView(w, "Root")

	scope := Scope{AccountID: "1", ProjectID: 42}
	_, cmd := w.Update(NavigateMsg{Target: ViewTodos, Scope: scope})
	require.NotNil(t, cmd, "navigate should return a batch command")

	// The factory should have been called, producing a new view
	require.Len(t, *viewLog, 1)
	assert.Equal(t, "Todos", (*viewLog)[0].title)

	// Router should now be depth 2
	assert.Equal(t, 2, w.router.Depth())
	assert.Equal(t, "Todos", w.router.Current().Title())
}

func TestWorkspace_RefreshForwards(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Root")

	w.Update(RefreshMsg{})

	require.NotEmpty(t, v.msgs)
	_, isRefresh := v.msgs[0].(RefreshMsg)
	assert.True(t, isRefresh, "RefreshMsg should be forwarded to current view")
}

func TestWorkspace_FocusBlurOnNav(t *testing.T) {
	w, viewLog := testWorkspace()
	root := pushTestView(w, "Root")

	// Navigate to a new view
	w.Update(NavigateMsg{Target: ViewTodos, Scope: Scope{}})

	// Root should have received a BlurMsg
	hasBlur := false
	for _, msg := range root.msgs {
		if _, ok := msg.(BlurMsg); ok {
			hasBlur = true
			break
		}
	}
	assert.True(t, hasBlur, "outgoing view should receive BlurMsg")

	// The navigate produces a FocusMsg via a batched command.
	// Verify the new view was created (it gets FocusMsg via the cmd, not directly).
	require.Len(t, *viewLog, 1)
	assert.Equal(t, "Todos", (*viewLog)[0].title)
}

func TestWorkspace_BreadcrumbJumpNoop(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")
	pushTestView(w, "Child")

	// Pressing "2" at depth 2 should be a no-op (same depth)
	w.handleKey(keyMsg("2"))
	assert.Equal(t, 2, w.router.Depth())
	assert.Equal(t, "Child", w.router.Current().Title())

	// Pressing "5" beyond depth should be a no-op
	w.handleKey(keyMsg("5"))
	assert.Equal(t, 2, w.router.Depth())
}

func TestWorkspace_BackSendsBlurAndFocus(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")
	child := pushTestView(w, "Child")

	w.handleKey(keyMsg("esc"))

	// Child should have received BlurMsg
	hasBlur := false
	for _, msg := range child.msgs {
		if _, ok := msg.(BlurMsg); ok {
			hasBlur = true
			break
		}
	}
	assert.True(t, hasBlur, "outgoing view should receive BlurMsg on back")

	// Root should have received FocusMsg (from goBack)
	root := w.router.Current().(*testView)
	hasFocus := false
	for _, msg := range root.msgs {
		if _, ok := msg.(FocusMsg); ok {
			hasFocus = true
			break
		}
	}
	assert.True(t, hasFocus, "restored view should receive FocusMsg on back")
}

func TestWorkspace_NewActionsRegistered(t *testing.T) {
	registry := DefaultActions()
	all := registry.All()

	// Verify new multi-account actions exist
	names := make(map[string]bool, len(all))
	for _, a := range all {
		names[a.Name] = true
	}

	assert.True(t, names[":pulse"], "pulse action should be registered")
	assert.True(t, names[":assignments"], "assignments action should be registered")
	assert.True(t, names[":pings"], "pings action should be registered")
}

func TestWorkspace_ActionsSearchFindsNew(t *testing.T) {
	registry := DefaultActions()

	results := registry.Search("activity")
	found := false
	for _, a := range results {
		if a.Name == ":pulse" {
			found = true
			break
		}
	}
	assert.True(t, found, "searching 'activity' should find :pulse action")
}

func TestWorkspace_AccountsDiscoveredRefreshesProjects(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Projects")

	// Discovery (any account count) should refresh Projects/Home so
	// identity-dependent pools (Assignments) get bootstrapped.
	w.Update(AccountsDiscoveredMsg{
		Accounts: []AccountInfo{
			{ID: "1", Name: "Only One"},
		},
	})

	hasRefresh := false
	for _, msg := range v.msgs {
		if _, ok := msg.(RefreshMsg); ok {
			hasRefresh = true
			break
		}
	}
	assert.True(t, hasRefresh, "Projects view should receive RefreshMsg after discovery")
}

func TestWorkspace_AccountsDiscoveredMultiNonProjectsNoRefresh(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Todos") // not "Projects"

	// Multiple accounts but current view is not Projects — no refresh
	_, cmd := w.Update(AccountsDiscoveredMsg{
		Accounts: []AccountInfo{
			{ID: "1", Name: "Alpha"},
			{ID: "2", Name: "Beta"},
		},
	})
	assert.Nil(t, cmd, "non-Projects view should not be refreshed")
}

func TestWorkspace_AccountsDiscoveredErrorSilent(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Projects")

	_, cmd := w.Update(AccountsDiscoveredMsg{
		Err: fmt.Errorf("network error"),
	})
	assert.Nil(t, cmd, "discovery errors should be silent")
}

// testSessionWithContext returns a Session with full context + scope lifecycle,
// suitable for testing account switch isolation and concurrency.
func testSessionWithContext(accountID, accountName string) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	ms := data.NewMultiStore(nil)
	return &Session{
		styles:     tui.NewStyles(),
		multiStore: ms,
		hub:        data.NewHub(ms, data.NewPoller()),
		ctx:        ctx,
		cancel:     cancel,
		scope:      Scope{AccountID: accountID, AccountName: accountName},
	}
}

// testWorkspaceWithSession builds a Workspace using the given session.
func testWorkspaceWithSession(session *Session) *Workspace {
	styles := session.Styles()
	return &Workspace{
		session:         session,
		router:          NewRouter(),
		styles:          styles,
		keys:            DefaultGlobalKeyMap(),
		registry:        DefaultActions(),
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles),
		viewFactory: func(target ViewTarget, _ *Session, scope Scope) View {
			return &testView{title: targetName(target)}
		},
		sidebarTargets: []ViewTarget{ViewActivity, ViewHome},
		sidebarIndex:   -1,
		width:          120,
		height:         40,
	}
}

// staleFetchResultMsg simulates a view-specific data msg returned by a stale Cmd.
// Workspace doesn't handle this — it gets forwarded to the current view.
type staleFetchResultMsg struct {
	AccountID string
	Items     []string
}

func TestWorkspace_AccountSwitchIsolation(t *testing.T) {
	session := testSessionWithContext("old-account", "Old")
	w := testWorkspaceWithSession(session)
	oldView := pushTestView(w, "Home")

	// 1. Stamp a Cmd as workspace would — captures current epoch (0).
	staleCmd := w.stampCmd(func() tea.Msg {
		return staleFetchResultMsg{
			AccountID: "old-account",
			Items:     []string{"old-item-1", "old-item-2"},
		}
	})

	// 2. Switch account while the Cmd is "in flight".
	//    switchAccount calls ResetContext which advances the epoch to 1.
	w.switchAccount("new-account", "New")

	newView := w.router.Current().(*testView)
	require.False(t, oldView == newView,
		"switch should create a fresh view (pointer identity)")

	// 3. Execute the stale Cmd — returns EpochMsg{Epoch: 0, Inner: ...}.
	staleMsg := staleCmd()
	_, cmd := w.Update(staleMsg)

	// 4. Assert: the stale msg was DROPPED — neither view received it.
	assert.Equal(t, "new-account", session.Scope().AccountID,
		"session scope must remain new-account")
	assert.NoError(t, session.Context().Err(),
		"new context must remain active")

	for _, m := range newView.msgs {
		if _, isFetch := m.(staleFetchResultMsg); isFetch {
			t.Fatal("new view must not receive stale msgs from old epoch")
		}
	}
	for _, m := range oldView.msgs {
		if _, isFetch := m.(staleFetchResultMsg); isFetch {
			t.Fatal("old view must not receive msgs after being detached by switch")
		}
	}
	_ = cmd
}

func TestWorkspace_EpochMatchedMsgDelivered(t *testing.T) {
	session := testSessionWithContext("acct-1", "Test")
	w := testWorkspaceWithSession(session)
	v := pushTestView(w, "Home")

	// Stamp a Cmd at the current epoch — no switch will occur.
	cmd := w.stampCmd(func() tea.Msg {
		return staleFetchResultMsg{
			AccountID: "acct-1",
			Items:     []string{"item-1"},
		}
	})

	msg := cmd()
	w.Update(msg)

	// The view SHOULD receive the msg (epoch matches).
	found := false
	for _, m := range v.msgs {
		if _, ok := m.(staleFetchResultMsg); ok {
			found = true
			break
		}
	}
	assert.True(t, found, "current-epoch msg must be delivered to view")
}

func TestSession_ScopeContextThreadSafety(t *testing.T) {
	session := testSessionWithContext("acct-0", "Initial")

	// Concurrent readers simulate Cmd goroutines accessing scope/context.
	var wg sync.WaitGroup
	const workers = 10
	const iterations = 1000

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				scope := session.Scope()
				_ = scope.AccountID
				ctx := session.Context()
				_ = ctx.Err()
				_ = session.HasAccount()
			}
		}()
	}

	// Main goroutine writes scope and resets context concurrently.
	for i := 0; i < iterations; i++ {
		session.SetScope(Scope{AccountID: fmt.Sprintf("acct-%d", i)})
		session.ResetContext()
	}

	wg.Wait()
	// Success = no race detector failures.
}

func TestWorkspace_StalePaletteExecDropped(t *testing.T) {
	session := testSessionWithContext("old-account", "Old")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Home")

	// Simulate a palette action that returns a stale async Cmd.
	// PaletteExecMsg.Cmd is stamped by workspace, then the account switches.
	innerCmd := func() tea.Msg {
		return staleFetchResultMsg{AccountID: "old-account", Items: []string{"stale"}}
	}
	_, cmd := w.Update(chrome.PaletteExecMsg{Cmd: innerCmd})
	require.NotNil(t, cmd, "stamped palette exec should return a cmd")

	// Switch account — epoch advances.
	w.switchAccount("new-account", "New")
	newView := w.router.Current().(*testView)

	// Execute the stale stamped Cmd and deliver its result.
	staleMsg := cmd()
	w.Update(staleMsg)

	for _, m := range newView.msgs {
		if _, ok := m.(staleFetchResultMsg); ok {
			t.Fatal("stale palette exec result must not reach new view")
		}
	}
}

func TestWorkspace_StaleQuickJumpExecDropped(t *testing.T) {
	session := testSessionWithContext("old-account", "Old")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Home")

	innerCmd := func() tea.Msg {
		return staleFetchResultMsg{AccountID: "old-account", Items: []string{"stale"}}
	}
	_, cmd := w.Update(chrome.QuickJumpExecMsg{Cmd: innerCmd})
	require.NotNil(t, cmd, "stamped quick-jump exec should return a cmd")

	w.switchAccount("new-account", "New")
	newView := w.router.Current().(*testView)

	staleMsg := cmd()
	w.Update(staleMsg)

	for _, m := range newView.msgs {
		if _, ok := m.(staleFetchResultMsg); ok {
			t.Fatal("stale quick-jump exec result must not reach new view")
		}
	}
}

func TestWorkspace_CrossAccountNavigateRotatesHubRealm(t *testing.T) {
	session := testSessionWithContext("account-A", "Alpha Corp")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Pings")

	hub := session.Hub()
	hub.EnsureAccount("account-A")

	// Verify starting state.
	assert.Equal(t, "account-A", session.Scope().AccountID)

	// Simulate cross-account navigation (Pings → Campfire on account B).
	scope := Scope{
		AccountID: "account-B",
		ProjectID: 42,
		ToolType:  "chat",
		ToolID:    99,
	}
	w.Update(NavigateMsg{Target: ViewCampfire, Scope: scope})

	// Hub account realm should have rotated to account-B.
	acctRealm := hub.Account()
	require.NotNil(t, acctRealm)
	assert.Equal(t, "account:account-B", acctRealm.Name(),
		"Hub account realm must rotate to target account")

	// Session scope should reflect the new account.
	assert.Equal(t, "account-B", session.Scope().AccountID)
}

func TestWorkspace_CrossAccountNavigateUpdatesAccountName(t *testing.T) {
	session := testSessionWithContext("account-A", "Alpha Corp")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Pings")

	hub := session.Hub()
	hub.EnsureAccount("account-A")

	// Seed discovered accounts so navigate() can resolve names.
	ms := session.MultiStore()
	ms.SetAccountsForTest([]data.AccountInfo{
		{ID: "account-A", Name: "Alpha Corp"},
		{ID: "account-B", Name: "Beta Inc"},
	})

	scope := Scope{
		AccountID: "account-B",
		ProjectID: 42,
		ToolType:  "chat",
		ToolID:    99,
	}
	w.Update(NavigateMsg{Target: ViewCampfire, Scope: scope})

	// Scope should have the resolved account name.
	assert.Equal(t, "Beta Inc", session.Scope().AccountName,
		"cross-account navigate must resolve and set account name")
}

func TestWorkspace_CrossAccountNavigateOverwritesStaleAccountName(t *testing.T) {
	session := testSessionWithContext("account-A", "Alpha Corp")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Pings")

	hub := session.Hub()
	hub.EnsureAccount("account-A")

	ms := session.MultiStore()
	ms.SetAccountsForTest([]data.AccountInfo{
		{ID: "account-A", Name: "Alpha Corp"},
		{ID: "account-B", Name: "Beta Inc"},
	})

	// Simulate view cloning scope and only overwriting AccountID —
	// AccountName still carries the old account's name ("Alpha Corp").
	scope := Scope{
		AccountID:   "account-B",
		AccountName: "Alpha Corp", // stale!
		ProjectID:   42,
		ToolType:    "chat",
		ToolID:      99,
	}
	w.Update(NavigateMsg{Target: ViewCampfire, Scope: scope})

	// Must overwrite stale name with correct one.
	assert.Equal(t, "Beta Inc", session.Scope().AccountName,
		"stale AccountName from cloned scope must be replaced with target account's name")
}

func TestWorkspace_SameAccountNavigateNoRealmTeardown(t *testing.T) {
	session := testSessionWithContext("account-A", "Alpha Corp")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Home")

	hub := session.Hub()
	hub.EnsureAccount("account-A")
	realm := hub.Account()
	realmCtx := realm.Context()

	// Navigate within the same account — realm should be reused, not torn down.
	scope := Scope{AccountID: "account-A", ProjectID: 42}
	w.Update(NavigateMsg{Target: ViewTodos, Scope: scope})

	assert.Same(t, realm, hub.Account(),
		"same-account navigate must reuse the account realm")
	assert.NoError(t, realmCtx.Err(),
		"same-account navigate must not cancel the realm context")
}

func TestWorkspace_ForwardNavigateToNonProjectLeavesRealm(t *testing.T) {
	session := testSessionWithContext("account-A", "Alpha Corp")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Campfire")

	hub := session.Hub()
	hub.EnsureAccount("account-A")
	hub.EnsureProject(42)
	projectCtx := hub.Project().Context()

	// Navigate forward to a non-project view (Hey) — should leave project realm.
	scope := Scope{AccountID: "account-A"}
	w.Update(NavigateMsg{Target: ViewHey, Scope: scope})

	assert.Nil(t, hub.Project(),
		"forward navigate to non-project must tear down project realm")
	assert.Error(t, projectCtx.Err(),
		"project realm context must be canceled")
}

func TestWorkspace_StaleAccountNameMsgDropped(t *testing.T) {
	session := testSessionWithContext("old-account", "Old")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Home")

	// Stamp the fetchAccountName Cmd at the current epoch.
	nameCmd := w.stampCmd(func() tea.Msg {
		return AccountNameMsg{Name: "Old Corp"}
	})

	// Switch account — epoch advances.
	w.switchAccount("new-account", "New")

	// Execute the stale Cmd and deliver through Update.
	staleMsg := nameCmd()
	w.Update(staleMsg)

	// The new account name must NOT have been overwritten.
	assert.Equal(t, "New", session.Scope().AccountName,
		"stale account name must not overwrite post-switch name")
}

func TestWorkspace_SyncChromeSetGlobalHints(t *testing.T) {
	w, _ := testWorkspace()
	w.relayout() // set width on chrome components
	pushTestView(w, "Home")

	// syncChrome was called by pushTestView. Verify global hints are set
	// by rendering the status bar and checking for the hint text.
	view := w.statusBar.View()
	assert.Contains(t, view, "help", "status bar should contain global '? help' hint")
	assert.Contains(t, view, "cmds", "status bar should contain global 'ctrl+p cmds' hint")
}

func TestWorkspace_ViewHintRefreshOnUpdate(t *testing.T) {
	w, _ := testWorkspace()
	w.relayout() // set width on chrome components

	// Create a view that returns dynamic hints.
	v := &dynamicHintView{title: "TestView"}
	w.router.Push(v, Scope{}, 0)
	w.syncChrome()

	// Initially no hints.
	v.hints = nil
	w.syncChrome()

	// Change hints and trigger a view update — replaceCurrentView should
	// pick up the new hints.
	v.hints = []key.Binding{
		key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "test")),
	}
	updated, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	w.replaceCurrentView(updated)

	view := w.statusBar.View()
	assert.Contains(t, view, "test", "view hints should update after replaceCurrentView")
}

// dynamicHintView is a test view with configurable ShortHelp.
type dynamicHintView struct {
	title string
	hints []key.Binding
}

func (v *dynamicHintView) Init() tea.Cmd                       { return nil }
func (v *dynamicHintView) Update(tea.Msg) (tea.Model, tea.Cmd) { return v, nil }
func (v *dynamicHintView) View() string                        { return v.title }
func (v *dynamicHintView) Title() string                       { return v.title }
func (v *dynamicHintView) ShortHelp() []key.Binding            { return v.hints }
func (v *dynamicHintView) FullHelp() [][]key.Binding           { return [][]key.Binding{v.hints} }
func (v *dynamicHintView) SetSize(int, int)                    {}

// pushTestViewWithTarget pushes a named testView with a specific ViewTarget.
func pushTestViewWithTarget(w *Workspace, title string, target ViewTarget) *testView {
	v := &testView{title: title}
	w.router.Push(v, w.session.Scope(), target)
	w.syncChrome()
	return v
}

// --- ViewTarget.IsGlobal tests ---

func TestViewTarget_IsGlobal(t *testing.T) {
	globals := []ViewTarget{ViewHome, ViewHey, ViewPulse, ViewAssignments,
		ViewPings, ViewProjects, ViewSearch, ViewActivity}
	for _, vt := range globals {
		assert.True(t, vt.IsGlobal(), "ViewTarget %d should be global", vt)
	}

	scoped := []ViewTarget{ViewDock, ViewTodos, ViewCampfire, ViewCards,
		ViewMessages, ViewMyStuff, ViewPeople, ViewDetail, ViewSchedule,
		ViewDocsFiles, ViewCheckins, ViewForwards, ViewCompose, ViewTimeline}
	for _, vt := range scoped {
		assert.False(t, vt.IsGlobal(), "ViewTarget %d should not be global", vt)
	}
}

// --- syncAccountBadge tests ---

func TestWorkspace_SyncAccountBadge_SingleAccount(t *testing.T) {
	w, _ := testWorkspace()
	w.session.SetScope(Scope{AccountID: "1", AccountName: "Acme Corp"})
	w.accountList = []AccountInfo{{ID: "1", Name: "Acme Corp"}}

	pushTestViewWithTarget(w, "Home", ViewHome)
	w.syncAccountBadge(ViewHome)

	assert.Equal(t, "Acme Corp", w.breadcrumb.AccountBadge())
	assert.False(t, w.breadcrumb.BadgeGlobal())
	assert.Equal(t, 0, w.breadcrumb.BadgeIndex())
}

func TestWorkspace_SyncAccountBadge_MultiGlobal(t *testing.T) {
	w, _ := testWorkspace()
	w.session.SetScope(Scope{AccountID: "1", AccountName: "Acme Corp"})
	w.accountList = []AccountInfo{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}

	pushTestViewWithTarget(w, "Home", ViewHome)
	w.syncAccountBadge(ViewHome)

	assert.Equal(t, "✱ All Accounts", w.breadcrumb.AccountBadge())
	assert.True(t, w.breadcrumb.BadgeGlobal())
}

func TestWorkspace_SyncAccountBadge_MultiScoped(t *testing.T) {
	w, _ := testWorkspace()
	w.session.SetScope(Scope{AccountID: "1", AccountName: "Acme Corp"})
	w.accountList = []AccountInfo{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}

	pushTestViewWithTarget(w, "Todos", ViewTodos)
	w.syncAccountBadge(ViewTodos)

	assert.Equal(t, "Acme Corp", w.breadcrumb.AccountBadge())
	assert.False(t, w.breadcrumb.BadgeGlobal())
	assert.Equal(t, 1, w.breadcrumb.BadgeIndex(), "first account should be index 1")
}

func TestWorkspace_SyncAccountBadge_MultiScopedNoName(t *testing.T) {
	w, _ := testWorkspace()
	w.session.SetScope(Scope{AccountID: "1"}) // no name yet
	w.accountList = []AccountInfo{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}

	pushTestViewWithTarget(w, "Todos", ViewTodos)
	w.syncAccountBadge(ViewTodos)

	// Should fall back to AccountID, not leave badge empty/stale
	assert.Equal(t, "1", w.breadcrumb.AccountBadge())
	assert.Equal(t, 1, w.breadcrumb.BadgeIndex())
}

func TestWorkspace_SyncAccountBadge_TransitionGlobalToScoped(t *testing.T) {
	w, _ := testWorkspace()
	w.session.SetScope(Scope{AccountID: "1", AccountName: "Acme Corp"})
	w.accountList = []AccountInfo{
		{ID: "1", Name: "Acme Corp"},
		{ID: "2", Name: "Beta Inc"},
	}

	// Start global
	pushTestViewWithTarget(w, "Home", ViewHome)
	w.syncAccountBadge(ViewHome)
	assert.True(t, w.breadcrumb.BadgeGlobal())

	// Navigate to scoped view
	pushTestViewWithTarget(w, "Todos", ViewTodos)
	w.syncAccountBadge(ViewTodos)
	assert.False(t, w.breadcrumb.BadgeGlobal())
	assert.Equal(t, 1, w.breadcrumb.BadgeIndex())
	assert.Equal(t, "Acme Corp", w.breadcrumb.AccountBadge())
}

// --- ctrl+a hint tests ---

func TestWorkspace_TabForwardedWhenSidebarInactive(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Search")

	// Sidebar is not open — tab should reach the view.
	require.False(t, w.sidebarActive())

	w.handleKey(tea.KeyMsg{Type: tea.KeyTab})

	found := false
	for _, m := range v.msgs {
		if km, ok := m.(tea.KeyMsg); ok && km.Type == tea.KeyTab {
			found = true
			break
		}
	}
	assert.True(t, found, "tab must be forwarded to view when sidebar is inactive")
}

func TestWorkspace_TabConsumedWhenSidebarActive(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Home")

	// Open sidebar — sets showSidebar, creates sidebarView, width >= 100.
	w.toggleSidebar()
	require.True(t, w.sidebarActive(), "sidebar should be active after toggle")
	require.False(t, w.sidebarFocused, "sidebar starts unfocused")

	// Tab should switch focus to sidebar, not reach the main view.
	cmd := w.handleKey(tea.KeyMsg{Type: tea.KeyTab})

	assert.True(t, w.sidebarFocused, "tab should switch focus to sidebar")
	assert.Nil(t, cmd, "switchSidebarFocus returns nil")

	// The main view should NOT have received the tab key.
	for _, m := range v.msgs {
		if km, ok := m.(tea.KeyMsg); ok && km.Type == tea.KeyTab {
			t.Fatal("main view must not receive tab when sidebar is active")
		}
	}
}

func TestWorkspace_CtrlAHintMultiAccountOnly(t *testing.T) {
	w, _ := testWorkspace()
	w.relayout()
	pushTestView(w, "Home")

	// Single account: no ctrl+a hint
	w.accountList = []AccountInfo{{ID: "1", Name: "Acme"}}
	w.syncChrome()
	view := w.statusBar.View()
	assert.NotContains(t, view, "switch", "single-account should not show ctrl+a hint")

	// Multiple accounts: ctrl+a hint visible
	w.accountList = []AccountInfo{
		{ID: "1", Name: "Acme"},
		{ID: "2", Name: "Beta"},
	}
	w.syncChrome()
	view = w.statusBar.View()
	assert.Contains(t, view, "switch", "multi-account should show ctrl+a hint")
}

// --- Origin context tests ---

func TestWorkspace_Navigate_StripsOriginFromSession(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")

	scope := Scope{
		AccountID:  "1",
		ProjectID:  42,
		OriginView: "Activity",
		OriginHint: "completed Todo",
	}
	w.Update(NavigateMsg{Target: ViewDetail, Scope: scope})

	// Session scope should NOT have origin fields
	assert.Empty(t, w.session.Scope().OriginView,
		"session scope must not carry OriginView after navigate")
	assert.Empty(t, w.session.Scope().OriginHint,
		"session scope must not carry OriginHint after navigate")
}

func TestWorkspace_Navigate_ViewScopeRetainsOrigin(t *testing.T) {
	styles := tui.NewStyles()
	session := testSession()
	var capturedScope Scope

	factory := func(target ViewTarget, _ *Session, scope Scope) View {
		capturedScope = scope
		return &testView{title: targetName(target)}
	}

	w := &Workspace{
		session:         session,
		router:          NewRouter(),
		styles:          styles,
		keys:            DefaultGlobalKeyMap(),
		registry:        DefaultActions(),
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles),
		viewFactory:     factory,
		sidebarTargets:  []ViewTarget{ViewActivity, ViewHome},
		sidebarIndex:    -1,
		width:           120,
		height:          40,
	}
	pushTestView(w, "Root")

	scope := Scope{
		AccountID:  "1",
		OriginView: "Activity",
		OriginHint: "completed Todo",
	}
	w.Update(NavigateMsg{Target: ViewDetail, Scope: scope})

	assert.Equal(t, "Activity", capturedScope.OriginView,
		"factory must receive scope with OriginView")
	assert.Equal(t, "completed Todo", capturedScope.OriginHint,
		"factory must receive scope with OriginHint")
}

func TestWorkspace_OriginDoesNotLeakAcrossNavigations(t *testing.T) {
	styles := tui.NewStyles()
	session := testSession()
	var capturedScopes []Scope

	factory := func(target ViewTarget, _ *Session, scope Scope) View {
		capturedScopes = append(capturedScopes, scope)
		return &testView{title: targetName(target)}
	}

	w := &Workspace{
		session:         session,
		router:          NewRouter(),
		styles:          styles,
		keys:            DefaultGlobalKeyMap(),
		registry:        DefaultActions(),
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles),
		viewFactory:     factory,
		sidebarTargets:  []ViewTarget{ViewActivity, ViewHome},
		sidebarIndex:    -1,
		width:           120,
		height:          40,
	}
	pushTestView(w, "Root")

	// First navigation with origin
	w.Update(NavigateMsg{Target: ViewDetail, Scope: Scope{
		AccountID:  "1",
		OriginView: "Activity",
		OriginHint: "completed Todo",
	}})

	// Second navigation without origin
	w.Update(NavigateMsg{Target: ViewSearch, Scope: Scope{
		AccountID: "1",
	}})

	require.Len(t, capturedScopes, 2)
	assert.Equal(t, "Activity", capturedScopes[0].OriginView)
	assert.Empty(t, capturedScopes[1].OriginView,
		"second navigation must not inherit stale OriginView")
	assert.Empty(t, capturedScopes[1].OriginHint,
		"second navigation must not inherit stale OriginHint")
}

// --- Sidebar cycling tests ---

func TestWorkspace_SidebarCyclesActivityHomeClosed(t *testing.T) {
	w, viewLog := testWorkspace()
	pushTestView(w, "Home")

	// 1st ctrl+b: opens Activity
	w.toggleSidebar()
	require.True(t, w.showSidebar)
	require.NotNil(t, w.sidebarView)
	assert.Equal(t, "Activity", w.sidebarView.Title())

	// 2nd ctrl+b: cycles to Home
	w.toggleSidebar()
	require.True(t, w.showSidebar)
	require.NotNil(t, w.sidebarView)
	assert.Equal(t, "Home", w.sidebarView.Title())

	// 3rd ctrl+b: closes
	w.toggleSidebar()
	assert.False(t, w.showSidebar)
	assert.Nil(t, w.sidebarView)
	assert.Equal(t, -1, w.sidebarIndex)

	_ = viewLog
}

func TestWorkspace_SidebarCycleResetOnClose(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Home")

	// Open → cycle → close
	w.toggleSidebar()
	w.toggleSidebar()
	w.toggleSidebar()
	assert.False(t, w.showSidebar)

	// Reopen — should start at index 0 (Activity) again
	w.toggleSidebar()
	require.True(t, w.showSidebar)
	require.NotNil(t, w.sidebarView)
	assert.Equal(t, "Activity", w.sidebarView.Title())
}

func TestWorkspace_SidebarCycleNarrowTerminal(t *testing.T) {
	w, _ := testWorkspace()
	w.width = 80 // below sidebarMinWidth (100)
	pushTestView(w, "Home")

	w.toggleSidebar()

	// Sidebar is logically open but not rendered
	assert.True(t, w.showSidebar, "sidebar should be logically open")
	assert.NotNil(t, w.sidebarView, "sidebar view should be created")
	assert.False(t, w.sidebarActive(), "sidebar should not be rendered at narrow width")
}

// dynamicTitleView is a test view whose Title() changes.
type dynamicTitleView struct {
	title string
	testView
}

func (v *dynamicTitleView) Title() string { return v.title }

func TestWorkspace_ChromeSyncMsg_UpdatesBreadcrumb(t *testing.T) {
	w, _ := testWorkspace()
	w.relayout() // set width on chrome components
	dv := &dynamicTitleView{title: "Docs & Files"}
	dv.testView.title = "Docs & Files"
	w.router.Push(dv, Scope{}, 0)
	w.syncChrome()

	// Assert rendered breadcrumb contains the initial title
	view := w.breadcrumb.View()
	assert.Contains(t, view, "Docs & Files", "breadcrumb should render initial title")

	// Change title dynamically and send ChromeSyncMsg
	dv.title = "Design Assets"
	w.Update(ChromeSyncMsg{})

	// Assert rendered breadcrumb now reflects the new title
	view = w.breadcrumb.View()
	assert.Contains(t, view, "Design Assets", "breadcrumb should render updated title after ChromeSyncMsg")
	assert.NotContains(t, view, "Docs & Files", "old title should no longer appear in breadcrumb")
}

func TestWorkspace_SidebarCycleWhileFocused(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Home")

	// Open sidebar and focus it
	w.toggleSidebar()
	require.True(t, w.sidebarActive())
	w.switchSidebarFocus()
	require.True(t, w.sidebarFocused)

	// ctrl+b should cycle AND reset focus to main
	w.toggleSidebar()
	assert.False(t, w.sidebarFocused, "cycling should reset sidebar focus to main")
	assert.True(t, w.showSidebar, "should still be showing sidebar (Home panel)")
}
