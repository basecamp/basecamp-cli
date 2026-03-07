package workspace

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

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
func (v *testView) Update(msg tea.Msg) (View, tea.Cmd) {
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
		boostPicker:     NewBoostPicker(styles),
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
		ViewProjects:       "Projects",
		ViewDock:           "Dock",
		ViewTodos:          "Todos",
		ViewCampfire:       "Campfire",
		ViewHey:            "Hey!",
		ViewCards:          "Cards",
		ViewMessages:       "Messages",
		ViewSearch:         "Search",
		ViewMyStuff:        "My Stuff",
		ViewPulse:          "Pulse",
		ViewAssignments:    "Assignments",
		ViewPings:          "Pings",
		ViewActivity:       "Activity",
		ViewTimeline:       "Project Activity",
		ViewHome:           "Home",
		ViewBonfireSidebar: "Chats",
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

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	if len(k) == 1 {
		return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
	}
	return tea.KeyPressMsg{Code: rune(k[0]), Text: k}
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

func TestWorkspace_BackAtRootRequiresDoublePress(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")

	// First esc shows confirmation toast
	cmd := w.handleKey(keyMsg("esc"))
	require.NotNil(t, cmd)
	assert.True(t, w.confirmQuit, "first esc should arm confirmQuit")
	assert.False(t, w.quitting, "should not quit on first esc")

	// Second esc actually quits
	cmd = w.handleKey(keyMsg("esc"))
	require.NotNil(t, cmd)
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "second Esc at root should quit")
	assert.True(t, w.quitting)
}

func TestWorkspace_BackspaceAlsoQuits(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")

	// First esc arms confirmation
	w.handleKey(keyMsg("esc"))
	assert.True(t, w.confirmQuit, "first esc should arm confirmQuit")

	// Backspace (also a Back binding) should work as second press
	cmd := w.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.NotNil(t, cmd)
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	assert.True(t, isQuit, "backspace after esc at root should quit")
}

func TestWorkspace_ConfirmQuit_ResetOnOtherKey(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")

	w.handleKey(keyMsg("esc"))
	assert.True(t, w.confirmQuit)

	// Non-back key resets
	w.handleKey(keyMsg("j"))
	assert.False(t, w.confirmQuit, "non-back key should reset confirmQuit")
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
	_, isKey := v.msgs[len(v.msgs)-1].(tea.KeyPressMsg)
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
	km, isKey := received.(tea.KeyPressMsg)
	assert.True(t, isKey)
	assert.Equal(t, tea.KeyEscape, km.Code)
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

func TestWorkspace_AccountsDiscoveredError_SurfacesStatus(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Projects")

	_, cmd := w.Update(AccountsDiscoveredMsg{
		Err: fmt.Errorf("network error"),
	})
	require.NotNil(t, cmd, "discovery errors should produce a status cmd")
	msg := cmd()
	status, ok := msg.(StatusMsg)
	require.True(t, ok, "should produce StatusMsg")
	assert.Equal(t, "Account discovery failed", status.Text)
	assert.True(t, status.IsError)
}

// testSessionWithContext returns a Session with full context + scope lifecycle,
// suitable for testing account switch isolation and concurrency.
func testSessionWithContext(accountID, accountName string) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	ms := data.NewMultiStore(nil)
	return &Session{
		styles:     tui.NewStyles(),
		multiStore: ms,
		hub:        data.NewHub(ms, ""),
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
		boostPicker:     NewBoostPicker(styles),
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

func TestSession_DarkBackgroundThreadSafety(t *testing.T) {
	session := NewTestSession()

	var wg sync.WaitGroup
	const workers = 10
	const iterations = 500

	// Concurrent readers call ReloadTheme (reads hasDarkBG under lock).
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				session.ReloadTheme()
			}
		}()
	}

	// Main goroutine writes hasDarkBG concurrently.
	for i := 0; i < iterations; i++ {
		session.SetDarkBackground(i%2 == 0)
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
	assert.Contains(t, view, "ctrl+p", "status bar should contain global ctrl+p hint")
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
	updated, _ := v.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	w.replaceCurrentView(updated)

	view := w.statusBar.View()
	assert.Contains(t, view, "test", "view hints should update after replaceCurrentView")
}

// dynamicHintView is a test view with configurable ShortHelp.
type dynamicHintView struct {
	title string
	hints []key.Binding
}

func (v *dynamicHintView) Init() tea.Cmd                  { return nil }
func (v *dynamicHintView) Update(tea.Msg) (View, tea.Cmd) { return v, nil }
func (v *dynamicHintView) View() string                   { return v.title }
func (v *dynamicHintView) Title() string                  { return v.title }
func (v *dynamicHintView) ShortHelp() []key.Binding       { return v.hints }
func (v *dynamicHintView) FullHelp() [][]key.Binding      { return [][]key.Binding{v.hints} }
func (v *dynamicHintView) SetSize(int, int)               {}

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

	w.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})

	found := false
	for _, m := range v.msgs {
		if km, ok := m.(tea.KeyPressMsg); ok && km.Code == tea.KeyTab {
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
	cmd := w.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})

	assert.True(t, w.sidebarFocused, "tab should switch focus to sidebar")
	_ = cmd // may be non-nil when blur/focus produce cmds

	// The main view should NOT have received the tab key.
	for _, m := range v.msgs {
		if km, ok := m.(tea.KeyPressMsg); ok && km.Code == tea.KeyTab {
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
		boostPicker:     NewBoostPicker(styles),
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
		boostPicker:     NewBoostPicker(styles),
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

// --- Auth error detection tests ---

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"SDK ErrAuth", basecamp.ErrAuth("Authentication failed"), true},
		{"SDK ErrAuth wrapped", fmt.Errorf("fetching: %w", basecamp.ErrAuth("token expired")), true},
		{"401 status code", fmt.Errorf("GET /projects.json: 401"), true},
		{"Unauthorized capitalized", fmt.Errorf("Unauthorized"), true},
		{"unauthorized lowercase", fmt.Errorf("unauthorized"), true},
		{"401 Unauthorized full", fmt.Errorf("401 Unauthorized"), true},
		{"normal error", fmt.Errorf("network timeout"), false},
		{"403 forbidden", fmt.Errorf("403 Forbidden"), false},
		{"500 server error", fmt.Errorf("500 Internal Server Error"), false},
		{"SDK ErrNotFound", basecamp.ErrNotFound("Project", "123"), false},
		{"SDK ErrForbidden", basecamp.ErrForbidden("Access denied"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isAuthError(tt.err))
		})
	}
}

func TestWorkspace_ErrorMsg_AuthExpiry_SetsStatus(t *testing.T) {
	w, _ := testWorkspace()
	w.relayout()
	pushTestView(w, "Home")

	_, cmd := w.Update(ErrorMsg{Err: fmt.Errorf("401 Unauthorized"), Context: "fetching todos"})

	// Auth errors should NOT produce a toast command — they set persistent status instead.
	assert.Nil(t, cmd, "auth error should not return a toast command")

	// Status bar should show the auth guidance.
	view := w.statusBar.View()
	assert.Contains(t, view, "Session expired", "status bar should show auth expiry guidance")
}

func TestWorkspace_ErrorMsg_NonAuth_ShowsToast(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Home")

	_, cmd := w.Update(ErrorMsg{Err: fmt.Errorf("network timeout"), Context: "fetching todos"})

	// Non-auth errors should produce a toast command.
	assert.NotNil(t, cmd, "non-auth error should produce a toast command")
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

	// Open → cycle through all → close
	w.toggleSidebar() // Activity
	w.toggleSidebar() // Home
	w.toggleSidebar() // closed
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
	assert.True(t, w.showSidebar, "should still be showing sidebar (Activity panel)")
}

// focusCmdView returns a tea.Cmd from FocusMsg so we can verify navigation captures it.
type focusCmdView struct {
	testView
	focusCmd tea.Cmd
}

func (v *focusCmdView) Update(msg tea.Msg) (View, tea.Cmd) {
	v.msgs = append(v.msgs, msg)
	if _, ok := msg.(FocusMsg); ok && v.focusCmd != nil {
		return v, v.focusCmd
	}
	return v, nil
}

type goBackFocusSentinel struct{}

func TestWorkspace_GoBack_CapturesFocusCmd(t *testing.T) {
	w, _ := testWorkspace()

	// Push a root view that returns a cmd on FocusMsg
	sentinel := func() tea.Msg { return goBackFocusSentinel{} }
	root := &focusCmdView{
		testView: testView{title: "Root"},
		focusCmd: sentinel,
	}
	w.router.Push(root, Scope{}, 0)

	// Push a child on top
	pushTestView(w, "Child")
	assert.Equal(t, 2, w.router.Depth())

	cmd := w.goBack()
	require.NotNil(t, cmd, "goBack should return batched cmd including FocusMsg result")

	// Execute the batched command and collect messages
	msgs := executeBatch(cmd)
	found := false
	for _, m := range msgs {
		if _, ok := m.(goBackFocusSentinel); ok {
			found = true
		}
	}
	assert.True(t, found, "goBack should propagate the FocusMsg cmd from the restored view")
}

// executeBatch recursively executes a tea.Cmd and collects all resulting messages,
// unwrapping EpochMsg and tea.BatchMsg along the way.
func executeBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var all []tea.Msg
		for _, c := range batch {
			all = append(all, executeBatch(c)...)
		}
		return all
	}
	if ep, ok := msg.(EpochMsg); ok {
		return []tea.Msg{ep.Inner}
	}
	return []tea.Msg{msg}
}

// blurCmdView returns a tea.Cmd from BlurMsg so we can verify navigate() captures it.
type blurCmdView struct {
	testView
	blurCmd tea.Cmd
}

func (v *blurCmdView) Update(msg tea.Msg) (View, tea.Cmd) {
	v.msgs = append(v.msgs, msg)
	if _, ok := msg.(BlurMsg); ok && v.blurCmd != nil {
		return v, v.blurCmd
	}
	return v, nil
}

type navigateBlurSentinel struct{}

func TestWorkspace_Navigate_CapturesBlurCmd(t *testing.T) {
	w, _ := testWorkspace()

	// Push a view that returns a cmd on BlurMsg
	sentinel := func() tea.Msg { return navigateBlurSentinel{} }
	outgoing := &blurCmdView{
		testView: testView{title: "Outgoing"},
		blurCmd:  sentinel,
	}
	w.router.Push(outgoing, Scope{}, 0)

	// Navigate to a new view — should capture BlurMsg cmd from outgoing
	cmd := w.navigate(ViewTodos, Scope{})
	require.NotNil(t, cmd, "navigate should return batched cmd including BlurMsg result")

	msgs := executeBatch(cmd)
	found := false
	for _, m := range msgs {
		if _, ok := m.(navigateBlurSentinel); ok {
			found = true
		}
	}
	assert.True(t, found, "navigate should propagate the BlurMsg cmd from the outgoing view")
}

func TestWorkspace_MutationErrorMsg_ForwardedToActiveView(t *testing.T) {
	w, _ := testWorkspace()
	view := pushTestView(w, "ActiveView")

	errMsg := data.MutationErrorMsg{
		Key: "test:pool",
		Err: fmt.Errorf("API failure"),
	}

	w.Update(errMsg)

	// The active view should receive the MutationErrorMsg so it can perform
	// view-specific rollback (e.g., Cards re-syncs kanban, Todos re-syncs list).
	found := false
	for _, m := range view.msgs {
		if me, ok := m.(data.MutationErrorMsg); ok && me.Key == "test:pool" {
			found = true
			break
		}
	}
	assert.True(t, found, "MutationErrorMsg should be forwarded to the active view, not intercepted at workspace level")
}

func TestHumanizeError_NetworkErrors(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`Get "https://x.com": dial tcp: no such host`, "Could not connect to Basecamp"},
		{`connection refused`, "Could not connect to Basecamp"},
		{`context deadline exceeded`, "Request timed out"},
		{`request timeout`, "Request timed out"},
		{`unexpected EOF`, "Connection interrupted"},
		{`connection reset by peer`, "Connection interrupted"},
		{`403 forbidden`, "Access denied"},
		{`404 not found`, "Not found"},
		{`500 internal server error`, "Basecamp is temporarily unavailable"},
		{`502 bad gateway`, "Basecamp is temporarily unavailable"},
		{`503 service unavailable`, "Basecamp is temporarily unavailable"},
	}
	for _, tt := range tests {
		got := humanizeError(fmt.Errorf("%s", tt.input))
		assert.Equal(t, tt.want, got, "humanizeError(%q)", tt.input)
	}
}

func TestHumanizeError_Passthrough(t *testing.T) {
	got := humanizeError(fmt.Errorf("something weird"))
	assert.Equal(t, "something weird", got)
}

func TestHumanizeError_Truncation(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := humanizeError(fmt.Errorf("%s", long))
	assert.Equal(t, 80, utf8.RuneCountInString(got), "long errors should be truncated to 80 chars")
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestWorkspace_AllAccountsSwitcher_NavigatesToHome(t *testing.T) {
	session := testSessionWithContext("acct-1", "Alpha")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Home")

	// Simulate "All Accounts" selection from the account switcher.
	_, cmd := w.Update(chrome.AccountSwitchedMsg{AccountID: "", AccountName: "All Accounts"})
	require.NotNil(t, cmd, "All-Accounts switch should return a navigate cmd")

	// navigate() pushes a new Home view onto the stack (does not replace root).
	assert.Equal(t, 2, w.router.Depth(), "navigate pushes onto existing stack")
	assert.Equal(t, "Home", w.router.Current().Title(), "current view should be Home")

	// Session scope must be reset to an empty AccountID so global views
	// fan-out across all accounts instead of scoping to a single one.
	assert.Empty(t, session.Scope().AccountID,
		"session scope AccountID must be empty after All Accounts switch")
}

// testFocusedView satisfies View and FocusedRecording for open-in-browser tests.
type testFocusedView struct {
	testView
	focused FocusedItemScope
}

func (v *testFocusedView) FocusedItem() FocusedItemScope { return v.focused }

func TestWorkspace_OpenInBrowser_UsesFocusedItemScope(t *testing.T) {
	session := testSessionWithContext("default-acct", "Default")
	session.SetScope(Scope{AccountID: "default-acct", ProjectID: 1})
	w := testWorkspaceWithSession(session)

	// Capture the scope that openFunc receives.
	var captured Scope
	w.openFunc = func(scope Scope) tea.Cmd {
		captured = scope
		return func() tea.Msg { return StatusMsg{Text: "spy"} }
	}

	// Push a view that implements FocusedRecording with cross-account context.
	fv := &testFocusedView{
		testView: testView{title: "Search"},
		focused: FocusedItemScope{
			AccountID:   "x-acct",
			ProjectID:   42,
			RecordingID: 100,
		},
	}
	w.router.Push(fv, Scope{}, 0)
	w.syncChrome()

	// Press 'o' to trigger open-in-browser.
	w.handleKey(keyMsg("o"))

	// The scope passed to openFunc should merge focused item values
	// over the session scope.
	assert.Equal(t, "x-acct", captured.AccountID,
		"AccountID should come from FocusedItem, not session")
	assert.Equal(t, int64(42), captured.ProjectID,
		"ProjectID should come from FocusedItem, not session")
	assert.Equal(t, int64(100), captured.RecordingID,
		"RecordingID should come from FocusedItem")
}

func TestWorkspace_OpenInBrowser_FallsBackToSessionScope(t *testing.T) {
	session := testSessionWithContext("sess-acct", "Session")
	session.SetScope(Scope{AccountID: "sess-acct", ProjectID: 7})
	w := testWorkspaceWithSession(session)

	var captured Scope
	w.openFunc = func(scope Scope) tea.Cmd {
		captured = scope
		return func() tea.Msg { return StatusMsg{Text: "spy"} }
	}

	// Push a plain testView (does NOT implement FocusedRecording).
	pushTestView(w, "Campfire")

	w.handleKey(keyMsg("o"))

	// Without FocusedRecording, the session scope should pass through unchanged.
	assert.Equal(t, "sess-acct", captured.AccountID,
		"AccountID should fall back to session scope")
	assert.Equal(t, int64(7), captured.ProjectID,
		"ProjectID should fall back to session scope")
	assert.Equal(t, int64(0), captured.RecordingID,
		"RecordingID should be zero when no focused item")
}

func TestWorkspace_OpenInBrowser_PartialFocusedOverride(t *testing.T) {
	session := testSessionWithContext("sess-acct", "Session")
	session.SetScope(Scope{AccountID: "sess-acct", ProjectID: 7})
	w := testWorkspaceWithSession(session)

	var captured Scope
	w.openFunc = func(scope Scope) tea.Cmd {
		captured = scope
		return func() tea.Msg { return StatusMsg{Text: "spy"} }
	}

	// Focused item only has RecordingID — AccountID and ProjectID stay zero,
	// so the session values should be preserved.
	fv := &testFocusedView{
		testView: testView{title: "Todos"},
		focused:  FocusedItemScope{RecordingID: 55},
	}
	w.router.Push(fv, Scope{}, 0)
	w.syncChrome()

	w.handleKey(keyMsg("o"))

	assert.Equal(t, "sess-acct", captured.AccountID,
		"AccountID should stay from session when focused has empty string")
	assert.Equal(t, int64(7), captured.ProjectID,
		"ProjectID should stay from session when focused has zero")
	assert.Equal(t, int64(55), captured.RecordingID,
		"RecordingID should come from focused item")
}

func TestWorkspace_BoostTarget_PreservesAccountID(t *testing.T) {
	session := testSessionWithContext("default-acct", "Default")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Hey!")

	target := BoostTarget{
		AccountID:   "cross-acct",
		ProjectID:   42,
		RecordingID: 100,
		Title:       "Some todo",
	}

	// OpenBoostPickerMsg stores the target and arms the picker.
	w.Update(OpenBoostPickerMsg{Target: target})

	assert.True(t, w.pickingBoost, "picker should be armed")
	assert.Equal(t, "cross-acct", w.boostTarget.AccountID,
		"boostTarget must preserve the cross-account AccountID")
	assert.Equal(t, int64(42), w.boostTarget.ProjectID,
		"boostTarget must preserve ProjectID")
	assert.Equal(t, int64(100), w.boostTarget.RecordingID,
		"boostTarget must preserve RecordingID")
}

func TestWorkspace_BoostEmoji_PassesCrossAccountTarget(t *testing.T) {
	session := testSessionWithContext("default-acct", "Default")
	w := testWorkspaceWithSession(session)
	pushTestView(w, "Pulse")

	// Spy on createBoostFunc to capture the target and emoji.
	var capturedTarget BoostTarget
	var capturedEmoji string
	w.createBoostFunc = func(target BoostTarget, emoji string) tea.Cmd {
		capturedTarget = target
		capturedEmoji = emoji
		return nil
	}

	// Arm the picker with a cross-account target.
	w.Update(OpenBoostPickerMsg{Target: BoostTarget{
		AccountID:   "other-acct",
		ProjectID:   99,
		RecordingID: 200,
		Title:       "Design doc",
	}})
	require.True(t, w.pickingBoost)

	// Simulate emoji selection — this calls createBoostFunc(w.boostTarget, emoji).
	w.Update(BoostSelectedMsg{Emoji: "🎉"})

	assert.False(t, w.pickingBoost, "picker should be disarmed after selection")
	assert.Equal(t, "🎉", capturedEmoji)
	assert.Equal(t, "other-acct", capturedTarget.AccountID,
		"createBoost must receive the cross-account AccountID")
	assert.Equal(t, int64(99), capturedTarget.ProjectID)
	assert.Equal(t, int64(200), capturedTarget.RecordingID)
}

func TestWorkspace_BoostPickerDismiss_ClearsState(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Todos")

	target := BoostTarget{
		AccountID:   "acct-1",
		ProjectID:   10,
		RecordingID: 50,
	}
	w.Update(OpenBoostPickerMsg{Target: target})
	require.True(t, w.pickingBoost)

	// Pressing Esc while the picker is open should dismiss it.
	w.handleKey(keyMsg("esc"))

	assert.False(t, w.pickingBoost, "Esc should dismiss the boost picker")
}

func TestWorkspace_ToastDoesNotChangeLayoutHeight(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")
	w.width = 80
	w.height = 24
	w.relayout()

	// Render without toast
	outputNoToast := w.View().Content
	linesNoToast := strings.Count(outputNoToast, "\n")

	// Show a toast and render again
	w.toast.Show("Todo completed!", false)
	require.True(t, w.toast.Visible())

	outputWithToast := w.View().Content
	linesWithToast := strings.Count(outputWithToast, "\n")

	assert.Equal(t, linesNoToast, linesWithToast, "toast overlay should not change total line count")
}

func TestWorkspace_TerminalFocusBlur_NilHub(t *testing.T) {
	w, _ := testWorkspace()
	// testWorkspace uses a session with no Hub — must not panic.
	assert.Nil(t, w.session.Hub())

	_, cmd := w.Update(tea.FocusMsg{})
	assert.Nil(t, cmd)

	_, cmd = w.Update(tea.BlurMsg{})
	assert.Nil(t, cmd)
}

func TestWorkspace_TerminalFocusBlur_WithHub(t *testing.T) {
	w, _ := testWorkspace()
	w.session = NewTestSessionWithHub()

	hub := w.session.Hub()
	require.NotNil(t, hub)
	hub.EnsureAccount("test")

	// Register a polling pool to observe terminal focus changes.
	pool := data.NewPool[int]("observe", data.PoolConfig{PollBase: 10 * time.Second}, nil)
	hub.Account().Register("observe", pool)

	assert.Equal(t, 10*time.Second, pool.PollInterval())

	// Blur: 4× interval.
	w.Update(tea.BlurMsg{})
	assert.Equal(t, 40*time.Second, pool.PollInterval())

	// Focus: back to base.
	w.Update(tea.FocusMsg{})
	assert.Equal(t, 10*time.Second, pool.PollInterval())
}

func TestWorkspace_BackgroundColorMsg(t *testing.T) {
	w, _ := testWorkspace()
	w.session = NewTestSessionWithHub()
	w.session.SetDarkBackground(false)

	// Zero-value BackgroundColorMsg reports IsDark()=true, flipping
	// hasDarkBG from false to true.
	_, cmd := w.Update(tea.BackgroundColorMsg{})
	assert.Nil(t, cmd)
	assert.True(t, w.session.hasDarkBG, "zero-color BackgroundColorMsg.IsDark()=true should set dark=true")
}

func TestWorkspace_ViewReportsFocus(t *testing.T) {
	w, _ := testWorkspace()
	w.session = NewTestSessionWithHub()

	// Push an initial view so View() renders something.
	view := &testView{title: "Test"}
	w.router.Push(view, Scope{}, ViewHome)
	w.syncChrome()

	v := w.View()
	assert.True(t, v.ReportFocus, "View should request terminal focus reporting")
}

// focusCmdView returns a sentinel cmd from TerminalFocusMsg to verify stamping.
type termFocusCmdView struct {
	testView
}

type termFocusSentinel struct{}

func (v *termFocusCmdView) Update(msg tea.Msg) (View, tea.Cmd) {
	v.msgs = append(v.msgs, msg)
	if _, ok := msg.(TerminalFocusMsg); ok {
		return v, func() tea.Msg { return termFocusSentinel{} }
	}
	return v, nil
}

func TestWorkspace_TerminalFocus_ForwardsToSidebar(t *testing.T) {
	w, _ := testWorkspace()
	w.session = NewTestSessionWithHub()
	pushTestView(w, "Main")

	// Manually activate sidebar with a testView we can inspect.
	sidebar := &testView{title: "Sidebar"}
	w.showSidebar = true
	w.sidebarView = sidebar

	require.True(t, w.sidebarActive())

	w.Update(tea.FocusMsg{})

	// Sidebar should have received TerminalFocusMsg.
	found := false
	for _, m := range sidebar.msgs {
		if _, ok := m.(TerminalFocusMsg); ok {
			found = true
		}
	}
	assert.True(t, found, "sidebar should receive TerminalFocusMsg on terminal focus")
}

func TestWorkspace_TerminalFocus_StampsCommands(t *testing.T) {
	session := testSessionWithContext("acct-1", "Test")
	w := testWorkspaceWithSession(session)

	// Use a view that returns a cmd from TerminalFocusMsg.
	mainView := &termFocusCmdView{testView: testView{title: "Main"}}
	w.router.Push(mainView, Scope{}, ViewHome)
	w.syncChrome()

	_, cmd := w.Update(tea.FocusMsg{})
	require.NotNil(t, cmd, "focus should return stamped cmd from main view")

	// The raw msg must be an EpochMsg — proves stampCmd was applied.
	msg := cmd()
	ep, ok := msg.(EpochMsg)
	require.True(t, ok, "focus cmd must produce EpochMsg (stamped), got %T", msg)
	assert.Equal(t, session.Epoch(), ep.Epoch)

	// Inner must be our sentinel.
	_, ok = ep.Inner.(termFocusSentinel)
	assert.True(t, ok, "EpochMsg inner should be termFocusSentinel, got %T", ep.Inner)
}

func TestWorkspace_TerminalFocus_StampsSidebarCommands(t *testing.T) {
	session := testSessionWithContext("acct-1", "Test")
	w := testWorkspaceWithSession(session)

	// Both main and sidebar return cmds so the batch doesn't collapse.
	mainView := &termFocusCmdView{testView: testView{title: "Main"}}
	w.router.Push(mainView, Scope{}, ViewHome)
	w.syncChrome()

	sidebar := &termFocusCmdView{testView: testView{title: "Sidebar"}}
	w.showSidebar = true
	w.sidebarView = sidebar

	_, cmd := w.Update(tea.FocusMsg{})
	require.NotNil(t, cmd, "focus should return stamped cmds from main+sidebar")

	// Both produce cmds, so result must be a BatchMsg.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	require.True(t, ok, "focus with two cmd-returning views should produce BatchMsg, got %T", msg)

	// Every non-nil batch member must be epoch-stamped.
	sentinelCount := 0
	for _, c := range batch {
		if c == nil {
			continue
		}
		m := c()
		ep, ok := m.(EpochMsg)
		require.True(t, ok, "each batch member must be EpochMsg (stamped), got %T", m)
		assert.Equal(t, session.Epoch(), ep.Epoch)
		if _, ok := ep.Inner.(termFocusSentinel); ok {
			sentinelCount++
		}
	}
	assert.Equal(t, 2, sentinelCount, "both main and sidebar sentinels should be epoch-stamped")
}

func TestPoolMonitorResizeRefocusesMainView(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	mainView := pushTestView(w, "Main")
	w.width = 120
	w.height = 40
	w.relayout()

	// Open pool monitor (unfocused), then focus it.
	w.togglePoolMonitor() // open unfocused
	w.togglePoolMonitor() // focus it
	require.True(t, w.poolMonitorFocused)

	// Main view should have received BlurMsg when monitor took focus.
	hasBlur := false
	for _, m := range mainView.msgs {
		if _, ok := m.(BlurMsg); ok {
			hasBlur = true
		}
	}
	require.True(t, hasBlur, "main view should be blurred when monitor is focused")

	// Clear message log so we can check what resize sends.
	mainView.msgs = nil

	// Resize narrow enough that the pool monitor can't fit (< minMainWidth + poolMonitorWidth + 1).
	w.Update(tea.WindowSizeMsg{Width: 70, Height: 40})

	assert.False(t, w.poolMonitorFocused, "monitor focus should be cleared on narrow resize")
	assert.False(t, w.poolMonitorActive(), "monitor should be inactive at narrow width")

	// Main view must have received FocusMsg so it resumes polling.
	hasFocus := false
	for _, m := range mainView.msgs {
		if _, ok := m.(FocusMsg); ok {
			hasFocus = true
		}
	}
	assert.True(t, hasFocus, "main view should be re-focused after monitor disappears on resize")
}

func TestGoToDepthClearsPoolMonitorFocus(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	pushTestView(w, "Root")
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Navigate deeper to create depth
	w.navigate(ViewDock, Scope{ProjectID: 1, AccountID: "a"})
	w.navigate(ViewTodos, Scope{ProjectID: 1, AccountID: "a"})

	// Open and focus pool monitor via 3-state toggle
	w.togglePoolMonitor() // open unfocused
	w.togglePoolMonitor() // focus it
	require.True(t, w.poolMonitorFocused)

	// Breadcrumb jump via goToDepth should clear monitor focus
	w.goToDepth(1)
	assert.False(t, w.poolMonitorFocused, "goToDepth should clear pool monitor focus")
	assert.True(t, w.poolMonitorActive(), "monitor should stay visible after goToDepth")

	// Main view should have received FocusMsg
	restored := w.router.Current().(*testView)
	hasFocus := false
	for _, m := range restored.msgs {
		if _, ok := m.(FocusMsg); ok {
			hasFocus = true
		}
	}
	assert.True(t, hasFocus, "restored view should receive FocusMsg after goToDepth")
}

func TestEscPopsPoolMonitorFocusBeforeNavigating(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	pushTestView(w, "Root")
	w.navigate(ViewDock, Scope{ProjectID: 1, AccountID: "a"})
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Open and focus pool monitor
	w.togglePoolMonitor()
	w.togglePoolMonitor() // focus it
	require.True(t, w.poolMonitorFocused)
	require.True(t, w.router.CanGoBack(), "should be able to go back")

	initialDepth := w.router.Depth()

	// First Esc: should pop focus, not navigate back
	w.handleKey(keyMsg("esc"))
	assert.False(t, w.poolMonitorFocused, "first Esc should clear monitor focus")
	assert.Equal(t, initialDepth, w.router.Depth(), "first Esc should not navigate back")
	assert.True(t, w.poolMonitorActive(), "monitor should stay visible")

	// Main view should have received FocusMsg
	mainView := w.router.Current().(*testView)
	hasFocus := false
	for _, m := range mainView.msgs {
		if _, ok := m.(FocusMsg); ok {
			hasFocus = true
		}
	}
	assert.True(t, hasFocus, "main view should receive FocusMsg after Esc pops monitor focus")

	// Second Esc: should navigate back
	w.handleKey(keyMsg("esc"))
	assert.Equal(t, initialDepth-1, w.router.Depth(), "second Esc should navigate back")
}

func TestEscPopsSidebarFocusBeforeNavigating(t *testing.T) {
	w, _ := testWorkspace()

	pushTestView(w, "Root")
	w.navigate(ViewDock, Scope{ProjectID: 1, AccountID: "a"})
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Open sidebar and focus it
	w.toggleSidebar()
	require.True(t, w.sidebarActive())
	w.switchSidebarFocus()
	require.True(t, w.sidebarFocused)

	initialDepth := w.router.Depth()

	// First Esc: should pop sidebar focus, not navigate back
	w.handleKey(keyMsg("esc"))
	assert.False(t, w.sidebarFocused, "first Esc should clear sidebar focus")
	assert.Equal(t, initialDepth, w.router.Depth(), "first Esc should not navigate back")

	// Second Esc: should navigate back
	w.handleKey(keyMsg("esc"))
	assert.Equal(t, initialDepth-1, w.router.Depth(), "second Esc should navigate back")
}

func TestInputActiveBacktickTogglesPoolMonitor(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	// Push a view that captures input
	inputView := pushTestView(w, "Search")
	inputView.inputActive = true
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Backtick during inputActive should open pool monitor
	w.handleKey(keyMsg("`"))
	assert.True(t, w.showPoolMonitor, "backtick during inputActive should open pool monitor")
	assert.False(t, w.poolMonitorFocused, "first backtick opens unfocused")

	// Second backtick focuses it (3-state: closed → open → focused → closed)
	w.handleKey(keyMsg("`"))
	assert.True(t, w.showPoolMonitor, "monitor still open")
	assert.True(t, w.poolMonitorFocused, "second backtick focuses")

	// Third backtick closes it
	w.handleKey(keyMsg("`"))
	assert.False(t, w.showPoolMonitor, "third backtick closes pool monitor")
}

func TestSwitchAccountClearsPoolMonitorFocus(t *testing.T) {
	session := testSessionWithContext("old-account", "Old")
	w := testWorkspaceWithSession(session)
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	pushTestView(w, "Root")
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Open and focus pool monitor
	w.togglePoolMonitor()
	w.togglePoolMonitor() // focus it
	require.True(t, w.poolMonitorFocused)

	// Switch account
	w.switchAccount("new-account", "New")

	assert.False(t, w.poolMonitorFocused, "switchAccount should clear pool monitor focus")
	assert.True(t, w.showPoolMonitor, "pool monitor should stay visible after switchAccount")
}

func TestEnterPassesThroughWhenPoolMonitorUnfocused(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	mainView := &testView{title: "Root"}
	w.router.Push(mainView, Scope{}, ViewHome)
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Open pool monitor but leave focus on main view
	w.togglePoolMonitor()
	require.True(t, w.showPoolMonitor)
	require.False(t, w.poolMonitorFocused, "monitor should open unfocused")

	mainView.msgs = nil
	w.handleKey(keyMsg("enter"))

	// Enter should reach the main view, not be consumed by the monitor
	found := false
	for _, m := range mainView.msgs {
		if km, ok := m.(tea.KeyPressMsg); ok && km.Code == tea.KeyEnter {
			found = true
		}
	}
	assert.True(t, found, "enter should reach main view when pool monitor is open but unfocused")

	// Now focus the monitor via toggle — enter should NOT reach main view
	w.togglePoolMonitor() // focuses it (3-state: open unfocused → focused)
	require.True(t, w.poolMonitorFocused)

	mainView.msgs = nil
	w.handleKey(keyMsg("enter"))

	found = false
	for _, m := range mainView.msgs {
		if km, ok := m.(tea.KeyPressMsg); ok && km.Code == tea.KeyEnter {
			found = true
		}
	}
	assert.False(t, found, "enter should be consumed by focused panel, not reach main view")
}

func TestTabReachesPoolMonitorWithoutSidebar(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	pushTestView(w, "Root")
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Open pool monitor (no sidebar)
	w.togglePoolMonitor()
	require.True(t, w.poolMonitorActive())
	require.False(t, w.sidebarActive(), "sidebar should be closed")
	require.False(t, w.poolMonitorFocused)

	// Tab should cycle to pool monitor
	w.handleKey(keyMsg("tab"))
	assert.True(t, w.poolMonitorFocused, "tab should focus pool monitor when only monitor is active")

	// Tab again returns to main
	w.handleKey(keyMsg("tab"))
	assert.False(t, w.poolMonitorFocused, "tab should cycle back to main")
}

func TestInputActiveTabCyclesToPoolMonitor(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	inputView := pushTestView(w, "Search")
	inputView.inputActive = true
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	// Open pool monitor
	w.togglePoolMonitor()
	require.True(t, w.poolMonitorActive())

	// Tab during inputActive should cycle to monitor
	w.handleKey(keyMsg("tab"))
	assert.True(t, w.poolMonitorFocused, "tab during inputActive should focus pool monitor")

	// Tab again should cycle back to main
	w.handleKey(keyMsg("tab"))
	assert.False(t, w.poolMonitorFocused, "tab should cycle back to main")
}

func TestNumberKeysConsumedByFocusedPanel(t *testing.T) {
	w, _ := testWorkspace()
	w.poolMonitorFactory = func() View { return &testView{title: "Monitor"} }

	pushTestView(w, "Root")
	w.navigate(ViewDock, Scope{ProjectID: 1, AccountID: "a"})
	w.navigate(ViewTodos, Scope{ProjectID: 1, AccountID: "a"})
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	w.relayout()

	initialDepth := w.router.Depth()
	require.Equal(t, 3, initialDepth)

	// Open and focus pool monitor
	w.togglePoolMonitor()
	w.togglePoolMonitor() // focus it
	require.True(t, w.poolMonitorFocused)

	// Press '1' — should go to pool monitor, NOT trigger goToDepth
	w.handleKey(keyMsg("1"))
	assert.Equal(t, initialDepth, w.router.Depth(),
		"number key should be consumed by focused panel, not trigger breadcrumb jump")
	assert.True(t, w.poolMonitorFocused, "pool monitor should stay focused")

	// Unfocus monitor — '1' should now trigger goToDepth
	w.switchSidebarFocus() // monitor → main
	require.False(t, w.poolMonitorFocused)
	w.handleKey(keyMsg("1"))
	assert.Equal(t, 1, w.router.Depth(), "number key without panel focus should jump to depth")
}

func TestDuplicateNavigationGuards(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Root")
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Navigate to Hey
	w.navigate(ViewHey, w.session.Scope())
	depth := w.router.Depth()

	// Hit ctrl+y again — should NOT push a duplicate
	w.handleKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	assert.Equal(t, depth, w.router.Depth(), "duplicate Hey should not grow stack")

	// Navigate to MyStuff
	w.navigate(ViewMyStuff, w.session.Scope())
	depth = w.router.Depth()

	// Hit ctrl+s again
	w.handleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Equal(t, depth, w.router.Depth(), "duplicate MyStuff should not grow stack")

	// Navigate to Activity
	w.navigate(ViewActivity, w.session.Scope())
	depth = w.router.Depth()

	// Hit ctrl+t again
	w.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	assert.Equal(t, depth, w.router.Depth(), "duplicate Activity should not grow stack")
}

func TestDuplicateNavigationGuardsInputActive(t *testing.T) {
	w, _ := testWorkspace()

	inputView := pushTestView(w, "Root")
	inputView.inputActive = true
	w.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Navigate to Hey
	w.navigate(ViewHey, w.session.Scope())
	heyView := w.router.Current().(*testView)
	heyView.inputActive = true
	depth := w.router.Depth()

	// ctrl+y again during inputActive — should NOT push duplicate
	w.handleKey(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	assert.Equal(t, depth, w.router.Depth(), "duplicate Hey during inputActive should not grow stack")

	// Navigate to MyStuff
	w.navigate(ViewMyStuff, w.session.Scope())
	msView := w.router.Current().(*testView)
	msView.inputActive = true
	depth = w.router.Depth()

	// ctrl+s again during inputActive
	w.handleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	assert.Equal(t, depth, w.router.Depth(), "duplicate MyStuff during inputActive should not grow stack")

	// Navigate to Activity
	w.navigate(ViewActivity, w.session.Scope())
	actView := w.router.Current().(*testView)
	actView.inputActive = true
	depth = w.router.Depth()

	// ctrl+t again during inputActive
	w.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	assert.Equal(t, depth, w.router.Depth(), "duplicate Activity during inputActive should not grow stack")
}
