package workspace

import (
	tea "charm.land/bubbletea/v2"
)

// errMsg is how a failed read reaches the model. The box it raises stands over
// the screen until the reader dismisses it.
type errMsg struct{ err error } //nolint:errname // bubbletea convention

func (e errMsg) Error() string { return e.err.Error() }

func fail(err error) tea.Cmd {
	return func() tea.Msg { return errMsg{err: err} }
}

// closeScreenMsg pops the screen that sent it. A screen cannot pop itself — the
// stack belongs to the model.
type closeScreenMsg struct{}

func closeScreen() tea.Cmd {
	return func() tea.Msg { return closeScreenMsg{} }
}

// ctrlCResetMsg forgets a first ctrl+c that was not followed by a second.
type ctrlCResetMsg struct{}

// spinnerTickMsg advances the loading animation.
type spinnerTickMsg struct{}
