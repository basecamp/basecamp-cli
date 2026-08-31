package workspace

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
)

// stubView stands in for a real screen: it records what the model asked of it
// and answers with whatever the test set.
type stubView struct {
	title    string
	body     string
	loading  bool
	bindings []helpBinding

	capturing bool
	handled   bool

	width  int
	height int

	initialized bool
	keys        []string
}

func (v *stubView) Init() tea.Cmd {
	v.initialized = true
	return nil
}

func (v *stubView) Update(tea.Msg) (tea.Cmd, bool) { return nil, false }

func (v *stubView) View() string { return v.body }

func (v *stubView) Title() string { return v.title }

func (v *stubView) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	v.keys = append(v.keys, msg.String())
	return nil
}

func (v *stubView) HelpBindings() []helpBinding { return v.bindings }

func (v *stubView) Resize(width, height int) {
	v.width = width
	v.height = height
}

func (v *stubView) Loading() bool { return v.loading }

func (v *stubView) CapturingInput() bool { return v.capturing }

func (v *stubView) HandleBack() bool { return v.handled }

func newTestModel(t *testing.T) model {
	t.Helper()
	t.Setenv("NO_COLOR", "1")

	m := newModelWithAccount(t, "1234567")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(model)
}

// newModelWithAccount builds the model before any window size has arrived, so a
// test can watch what the account it was handed decides about the first screen.
func newModelWithAccount(t *testing.T, accountID string) model {
	t.Helper()

	cfg := config.Default()
	cfg.AccountID = accountID
	m := newModel(&appctx.App{Config: cfg})
	t.Cleanup(m.cancel)
	return m
}

func keyPress(key string) tea.KeyPressMsg {
	k := tea.Key{Text: key}
	switch key {
	case "ctrl+c":
		k = tea.Key{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+a":
		k = tea.Key{Code: 'a', Mod: tea.ModCtrl}
	case "ctrl+r":
		k = tea.Key{Code: 'r', Mod: tea.ModCtrl}
	case "ctrl+j":
		k = tea.Key{Code: 'j', Mod: tea.ModCtrl}
	case "ctrl+k":
		k = tea.Key{Code: 'k', Mod: tea.ModCtrl}
	case "esc":
		k = tea.Key{Code: tea.KeyEscape}
	case "enter":
		k = tea.Key{Code: tea.KeyEnter}
	case "alt+enter":
		k = tea.Key{Code: tea.KeyEnter, Mod: tea.ModAlt}
	case "tab":
		k = tea.Key{Code: tea.KeyTab}
	case "backspace":
		k = tea.Key{Code: tea.KeyBackspace}
	case "up":
		k = tea.Key{Code: tea.KeyUp}
	case "down":
		k = tea.Key{Code: tea.KeyDown}
	case "left":
		k = tea.Key{Code: tea.KeyLeft}
	case "right":
		k = tea.Key{Code: tea.KeyRight}
	default:
		runes := []rune(key)
		if len(runes) == 1 {
			k = tea.Key{Code: runes[0], Text: key}
		}
	}
	return tea.KeyPressMsg(k)
}

// screen is what the reader sees, with the escape sequences taken out so an
// assertion reads the way the terminal does.
func screen(m model) string {
	return ansi.Strip(m.View().Content)
}

// columnOf is the display column text starts at on a line. strings.Index counts
// bytes, and these rows are full of multi-byte box drawing — so the prefix is
// measured rather than counted.
func columnOf(t *testing.T, line, text string) int {
	t.Helper()

	at := strings.Index(line, text)
	require.GreaterOrEqual(t, at, 0, "no %q in %q", text, line)
	return lipgloss.Width(line[:at])
}

// deliver feeds one command's message back into the model, the way the Bubble
// Tea loop does. It stops there rather than following what that produces in
// turn: a tick command would make the test sit out its timer.
func deliver(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	require.NotNil(t, cmd)

	msg := cmd()
	require.NotNil(t, msg)
	updated, _ := m.Update(msg)
	return updated.(model)
}

func press(t *testing.T, m model, key string) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(keyPress(key))
	return updated.(model), cmd
}

func TestOpensOnHome(t *testing.T) {
	m := newTestModel(t)

	assert.Equal(t, "Home", m.nav.current().Title())

	view := screen(m)
	assert.Contains(t, view, "1234567 "+chevronClosed)
	assert.Contains(t, view, "ctrl+c ctrl+c quit")
	assert.Contains(t, view, "Recent activity")
}

// A screen is handed its container, not the terminal: the chrome above and below
// it takes its rows, and the sidebar beside it takes its columns.
func TestWindowSizeGivesTheViewWhatTheFrameLeaves(t *testing.T) {
	m := newTestModel(t)

	home := m.nav.current().(*home)
	assert.Equal(t, m.contentWidth(), home.width)
	assert.Equal(t, m.contentHeight(), home.height)
	assert.Less(t, home.width, 80)
	assert.Less(t, home.height, 24)
}

func TestPushAndPop(t *testing.T) {
	m := newTestModel(t)
	projects := &stubView{title: "Projects", body: "the projects"}

	m.push(projects)
	assert.True(t, projects.initialized)
	assert.Equal(t, m.contentWidth(), projects.width)
	assert.Equal(t, "the projects", m.screenView())
	assert.Contains(t, screen(m), "Home › Projects")
	assert.Contains(t, screen(m), "esc/q back")

	m, _ = press(t, m, "esc")
	assert.Equal(t, "Home", m.nav.current().Title())
	assert.NotContains(t, screen(m), "esc/q back")
}

func TestEscapeAtHomeDoesNothing(t *testing.T) {
	m := newTestModel(t)

	m, cmd := press(t, m, "esc")
	assert.Nil(t, cmd)
	assert.Equal(t, 1, m.nav.depth())
}

// A view with something of its own to close answers esc itself, and the screen
// stays on the stack.
func TestEscapeGoesToAViewThatClaimsIt(t *testing.T) {
	m := newTestModel(t)
	m.push(&stubView{title: "Projects", handled: true})

	m, _ = press(t, m, "esc")
	assert.Equal(t, 2, m.nav.depth())
}

// A view with an open text field gets every key, shortcuts included.
func TestCapturingViewGetsEveryKey(t *testing.T) {
	m := newTestModel(t)
	view := &stubView{title: "Compose", capturing: true}
	m.push(view)

	for _, key := range []string{"q", "?", "esc"} {
		m, _ = press(t, m, key)
	}

	assert.Equal(t, []string{"q", "?", "esc"}, view.keys)
	assert.Equal(t, 2, m.nav.depth())
}

func TestQuitTakesTwoCtrlC(t *testing.T) {
	m := newTestModel(t)

	m, cmd := press(t, m, "ctrl+c")
	require.NotNil(t, cmd)
	assert.True(t, m.ctrlCOnce)
	assert.Contains(t, screen(m), "press again to quit")

	_, cmd = press(t, m, "ctrl+c")
	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd())
}

// A first ctrl+c that is not followed by a second is forgotten, so the next one
// starts the chord over rather than quitting.
func TestCtrlCResets(t *testing.T) {
	m := newTestModel(t)
	m, _ = press(t, m, "ctrl+c")

	updated, _ := m.Update(ctrlCResetMsg{})
	m = updated.(model)
	assert.False(t, m.ctrlCOnce)

	m, cmd := press(t, m, "ctrl+c")
	require.NotNil(t, cmd)
	assert.NotEqual(t, tea.QuitMsg{}, cmd())
	assert.True(t, m.ctrlCOnce)
}

func TestErrorBoxCoversTheScreenUntilDismissed(t *testing.T) {
	m := newTestModel(t)
	view := &stubView{title: "Projects"}
	m.push(view)

	updated, _ := m.Update(errMsg{err: errors.New("the network is unreachable")})
	m = updated.(model)

	assert.Contains(t, screen(m), "the network is unreachable")
	assert.Contains(t, screen(m), "esc/q dismiss")

	// Nothing beneath the box acts while it is up.
	m, _ = press(t, m, "j")
	assert.Empty(t, view.keys)

	m, _ = press(t, m, "esc")
	assert.Nil(t, m.err)
	assert.Equal(t, 2, m.nav.depth())
}

func TestQuestionMarkHidesAndShowsTheHelpBar(t *testing.T) {
	m := newTestModel(t)
	contentHeight := m.contentHeight()

	m, _ = press(t, m, "?")
	assert.True(t, m.help.hidden)
	assert.NotContains(t, screen(m), "ctrl+c ctrl+c quit")
	assert.Greater(t, m.contentHeight(), contentHeight)

	m, _ = press(t, m, "?")
	assert.False(t, m.help.hidden)
	assert.Contains(t, screen(m), "ctrl+c ctrl+c quit")
	assert.Equal(t, contentHeight, m.contentHeight())
}

func TestToast(t *testing.T) {
	m := newTestModel(t)

	updated, cmd := m.Update(notifyMsg{text: "Todo completed"})
	m = updated.(model)
	require.NotNil(t, cmd)
	assert.Contains(t, screen(m), "Todo completed")

	updated, _ = m.Update(toastExpiredMsg{id: m.toastID})
	m = updated.(model)
	assert.NotContains(t, screen(m), "Todo completed")
}

// A second toast raised while the first is up must not be cleared by the first
// one's timer.
func TestStaleToastTimerIsIgnored(t *testing.T) {
	m := newTestModel(t)

	updated, _ := m.Update(notifyMsg{text: "first"})
	m = updated.(model)
	firstID := m.toastID

	updated, _ = m.Update(notifyMsg{text: "second"})
	m = updated.(model)

	updated, _ = m.Update(toastExpiredMsg{id: firstID})
	m = updated.(model)
	assert.Contains(t, screen(m), "second")
}

func TestNotifyCommands(t *testing.T) {
	assert.Equal(t, notifyMsg{text: "saved"}, notify("saved")())

	raised := notifyError("Could not load the projects", errors.New("boom"))().(notifyMsg)
	assert.Equal(t, toastError, raised.kind)
	assert.Contains(t, raised.text, "Could not load the projects")

	failed := fail(errors.New("boom"))().(errMsg)
	assert.Equal(t, "boom", failed.Error())
}

// The spinner is armed when a view starts waiting and disarmed when it stops,
// so an idle workspace never wakes up.
func TestSpinnerFollowsTheView(t *testing.T) {
	m := newTestModel(t)
	view := &stubView{title: "Projects", loading: true}

	cmd := m.push(view)
	require.NotNil(t, cmd)
	assert.True(t, m.loading)
	assert.Contains(t, m.screenView(), loadingLabel)

	updated, tick := m.Update(spinnerTickMsg{})
	m = updated.(model)
	require.NotNil(t, tick)
	assert.Equal(t, 1, m.spinnerPhase)

	view.loading = false
	m.syncLoading(nil)
	assert.False(t, m.loading)

	updated, tick = m.Update(spinnerTickMsg{})
	assert.Nil(t, tick)
	assert.Equal(t, 1, updated.(model).spinnerPhase)
}

// Keys are held while a read is in flight: the screen underneath is not the one
// the answer will draw.
func TestKeysAreHeldWhileLoading(t *testing.T) {
	m := newTestModel(t)
	view := &stubView{title: "Projects", loading: true}
	m.push(view)

	m, _ = press(t, m, "j")
	assert.Empty(t, view.keys)
}

func TestHelpBindingsComeFromTheViewOnTop(t *testing.T) {
	m := newTestModel(t)
	m.push(&stubView{title: "Todos", bindings: []helpBinding{{"space", "complete"}}})

	view := screen(m)
	assert.Contains(t, view, "space complete")
	assert.Contains(t, view, "esc/q back")
}

// The help bar decides the content height, so a screen whose bindings wrap onto
// a second line has to be told it has one row less to draw in.
func TestBindingsThatWrapCostTheViewARow(t *testing.T) {
	m := newTestModel(t)
	roomy := &stubView{title: "Todos"}
	m.push(roomy)

	crowded := &stubView{title: "Cards", bindings: []helpBinding{
		{"space", "complete"},
		{"c", "comment"},
		{"m", "move"},
		{"a", "assign"},
		{"d", "due date"},
		{"ctrl+r", "refresh"},
	}}
	m.push(crowded)

	assert.Equal(t, 2, m.help.height())
	assert.Equal(t, roomy.height-1, crowded.height)
	assert.Equal(t, crowded.height, m.contentHeight())
}

func TestThemeFollowsTheTerminalBackground(t *testing.T) {
	m := newTestModel(t)
	m.theme.Dark = true

	updated, _ := m.Update(tea.BackgroundColorMsg{Color: lightBackground{}})
	m = updated.(model)

	assert.False(t, m.theme.Dark)
	assert.False(t, m.styles.Theme().Dark)
}

// lightBackground is a color the terminal reports that reads as light.
type lightBackground struct{}

func (lightBackground) RGBA() (r, g, b, a uint32) { return 0xffff, 0xffff, 0xffff, 0xffff }

func TestViewFitsTheTerminal(t *testing.T) {
	m := newTestModel(t)

	lines := strings.Split(screen(m), "\n")
	assert.LessOrEqual(t, len(lines), 24)
}
