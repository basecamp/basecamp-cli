package views

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

// testComposeView builds a Compose view without a session for structural tests.
// The session-dependent fields (SDK client, upload function) are nil — tests
// that exercise submit/post are not covered here.
func testComposeView() *Compose {
	styles := tui.NewStyles()

	subj := textinput.New()
	subj.Placeholder = "Subject"
	subj.CharLimit = 256
	subj.Focus()

	comp := widget.NewComposer(styles,
		widget.WithMode(widget.ComposerRich),
		widget.WithAutoExpand(false),
		widget.WithPlaceholder("Message body (Markdown supported)..."),
	)

	return &Compose{
		styles:      styles,
		keys:        defaultComposeKeyMap(),
		subject:     subj,
		composer:    comp,
		focus:       composeFocusSubject,
		composeType: workspace.ComposeMessage,
		width:       80,
		height:      24,
	}
}

// --- InputActive ---

func TestCompose_InputActiveAlways(t *testing.T) {
	v := testComposeView()

	// InputActive returns true regardless of which field is focused.
	assert.True(t, v.InputActive(), "InputActive should be true when subject focused")

	v.toggleFocus()
	assert.True(t, v.InputActive(), "InputActive should be true when body focused")

	v.toggleFocus()
	assert.True(t, v.InputActive(), "InputActive should be true after toggling back to subject")
}

// --- IsModal ---

func TestCompose_IsModalAlways(t *testing.T) {
	v := testComposeView()

	assert.True(t, v.IsModal(), "IsModal should always be true for compose view")

	v.toggleFocus()
	assert.True(t, v.IsModal(), "IsModal should remain true after toggling focus")
}

// --- Tab switches focus ---

func TestCompose_TabSwitchesFocus(t *testing.T) {
	v := testComposeView()
	require.Equal(t, composeFocusSubject, v.focus, "initial focus should be on subject")

	// Tab switches to body
	v.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, composeFocusBody, v.focus, "Tab should switch focus to body")

	// Tab switches back to subject
	v.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, composeFocusSubject, v.focus, "second Tab should return focus to subject")
}

// --- Empty subject shows error ---

func TestCompose_EmptySubjectShowsError(t *testing.T) {
	v := testComposeView()

	// Subject is empty, body has content.
	v.composer.SetValue("Some body text")

	cmd := v.submit()
	require.NotNil(t, cmd, "submit with empty subject should return a cmd")

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg")
	assert.Contains(t, status.Text, "Subject is required")
	assert.True(t, status.IsError, "should be an error status")
}

func TestCompose_EmptyBodyShowsError(t *testing.T) {
	v := testComposeView()

	// Subject has content, body is empty.
	v.subject.SetValue("My Subject")

	cmd := v.submit()
	require.NotNil(t, cmd, "submit with empty body should return a cmd")

	msg := cmd()
	status, ok := msg.(workspace.StatusMsg)
	require.True(t, ok, "should produce StatusMsg for empty body")
	assert.Contains(t, status.Text, "Message body is required")
	assert.True(t, status.IsError)
}

// --- Esc navigates back ---

func TestCompose_EscNavigatesBack(t *testing.T) {
	v := testComposeView()

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd, "Esc should produce a cmd")

	msg := cmd()
	_, ok := msg.(workspace.NavigateBackMsg)
	assert.True(t, ok, "Esc should produce NavigateBackMsg")
}

func TestCompose_EscNavigatesBack_FromBody(t *testing.T) {
	v := testComposeView()
	v.toggleFocus()
	require.Equal(t, composeFocusBody, v.focus)

	cmd := v.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	require.NotNil(t, cmd)

	msg := cmd()
	_, ok := msg.(workspace.NavigateBackMsg)
	assert.True(t, ok, "Esc from body should also produce NavigateBackMsg")
}

// --- Title ---

func TestCompose_Title(t *testing.T) {
	v := testComposeView()
	assert.Equal(t, "New Message", v.Title())
}

// --- ShortHelp ---

func TestCompose_ShortHelp(t *testing.T) {
	v := testComposeView()
	hints := v.ShortHelp()

	require.Len(t, hints, 3)
	assert.Equal(t, "ctrl+enter", hints[0].Help().Key)
	assert.Equal(t, "send", hints[0].Help().Desc)
	assert.Equal(t, "tab", hints[1].Help().Key)
	assert.Equal(t, "switch field", hints[1].Help().Desc)
	assert.Equal(t, "esc", hints[2].Help().Key)
	assert.Equal(t, "cancel", hints[2].Help().Desc)
}

// --- Sending blocks key input ---

func TestCompose_SendingBlocksKeys(t *testing.T) {
	v := testComposeView()
	v.sending = true

	// handleKey is not called when sending — Update returns nil early.
	// We test via Update to match the real path.
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	assert.Nil(t, cmd, "keys should be blocked while sending")
}

// --- ComposerSubmitMsg error clears sending lock ---

func TestCompose_SubmitError_ClearsSending(t *testing.T) {
	v := testComposeView()
	v.sending = true

	// Simulate a ComposerSubmitMsg with an error (e.g. upload failure)
	_, cmd := v.Update(widget.ComposerSubmitMsg{Err: assert.AnError})
	require.NotNil(t, cmd, "error should produce an error report cmd")
	assert.False(t, v.sending, "sending should be cleared on ComposerSubmitMsg error")

	// Keys should be unblocked — tab should switch focus
	_, cmd = v.Update(tea.KeyMsg{Type: tea.KeyTab})
	assert.NotNil(t, cmd, "keys should be unblocked after error clears sending")
	assert.Equal(t, composeFocusBody, v.focus, "tab should switch focus when not sending")
}
