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

	factory := func(target ViewTarget, _ *Session, _ *data.Store, scope Scope) View {
		v := &testView{title: targetName(target)}
		log = append(log, v)
		return v
	}

	w = &Workspace{
		session:         session,
		router:          NewRouter(),
		store:           data.NewStore(),
		styles:          styles,
		keys:            DefaultGlobalKeyMap(),
		registry:        DefaultActions(),
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles, nil),
		viewFactory:     factory,
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
	}
	if n, ok := names[t]; ok {
		return n
	}
	return "Unknown"
}

// pushTestView is a helper that pushes a named testView onto the workspace router.
func pushTestView(w *Workspace, title string) *testView {
	v := &testView{title: title}
	w.router.Push(v, Scope{})
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

func TestWorkspace_AccountsDiscoveredSingleNoRefresh(t *testing.T) {
	w, _ := testWorkspace()
	pushTestView(w, "Projects") // matches Title() == "Projects"

	// Single account — should not trigger refresh
	_, cmd := w.Update(AccountsDiscoveredMsg{
		Accounts: []AccountInfo{
			{ID: "1", Name: "Only One"},
		},
	})
	assert.Nil(t, cmd, "single account should not trigger refresh")
}

func TestWorkspace_AccountsDiscoveredMultiRefreshesProjects(t *testing.T) {
	w, _ := testWorkspace()
	v := pushTestView(w, "Projects")

	// Multiple accounts — should refresh the Projects view
	w.Update(AccountsDiscoveredMsg{
		Accounts: []AccountInfo{
			{ID: "1", Name: "Alpha"},
			{ID: "2", Name: "Beta"},
		},
	})

	// The view should have received a RefreshMsg
	hasRefresh := false
	for _, msg := range v.msgs {
		if _, ok := msg.(RefreshMsg); ok {
			hasRefresh = true
			break
		}
	}
	assert.True(t, hasRefresh, "Projects view should receive RefreshMsg")
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
	store := data.NewStore()
	return &Workspace{
		session:         session,
		router:          NewRouter(),
		store:           store,
		styles:          styles,
		keys:            DefaultGlobalKeyMap(),
		registry:        DefaultActions(),
		statusBar:       chrome.NewStatusBar(styles),
		breadcrumb:      chrome.NewBreadcrumb(styles),
		toast:           chrome.NewToast(styles),
		help:            chrome.NewHelp(styles),
		palette:         chrome.NewPalette(styles),
		accountSwitcher: chrome.NewAccountSwitcher(styles, nil),
		viewFactory: func(target ViewTarget, _ *Session, _ *data.Store, scope Scope) View {
			return &testView{title: targetName(target)}
		},
		width:  120,
		height: 40,
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
	require.NotEqual(t, oldView, newView,
		"switch should create a fresh view")

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
