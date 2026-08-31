package workspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openEdit is the edit form open over the folder it edits.
func openEdit(t *testing.T, edited folder) (model, *folderEdit) {
	t.Helper()

	m := resize(t, newTestModel(t), 80, 24)
	l := newFolder(m.ctx, edited)
	m.push(l)
	// Enough projects that some are above the box as well as behind it: the
	// point of a modal is that the screen it interrupted is still there.
	m = load(t, m, directoryLoadedMsg{projects: []project{
		{id: 1, name: "Ops: On Call"},
		{id: 2, name: "QA"},
		{id: 3, name: "App Security"},
		{id: 4, name: "AI Labs"},
	}})

	m = load(t, m, editFolderMsg{folder: edited})
	form, ok := m.modal.(*folderEdit)
	require.True(t, ok, "the edit form did not open")
	return m, form
}

func testFolderToEdit() folder {
	return folder{id: 7, name: "Teams", color: "blue", projects: []int64{1}}
}

// --- The modal seam ---

// A form is not a place: it opens over the folder rather than being walked into,
// so the breadcrumb still says where the reader is and the folder stays on
// screen around the border.
func TestTheEditFormOpensOverTheFolder(t *testing.T) {
	m, _ := openEdit(t, testFolderToEdit())

	rendered := ansi.Strip(screen(m))
	assert.Contains(t, rendered, "Edit folder")
	assert.Contains(t, rendered, "AI Labs", "the folder was hidden behind its own form")
	assert.Equal(t, []string{"Home", "Teams"}, m.nav.trail())
	assert.Equal(t, 2, m.nav.depth())
}

// A modal holds every key while it is up — the jump menu's included.
func TestAModalKeepsEveryKey(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	m, _ = press(t, m, menuKey)
	assert.False(t, m.menu.open, "the menu opened from under a modal")

	// Letters are letters, not shortcuts.
	_, _ = press(t, m, "x")
	assert.Equal(t, "Teamsx", form.name.Value())
}

func TestEscapeClosesTheForm(t *testing.T) {
	m, _ := openEdit(t, testFolderToEdit())

	m, _ = press(t, m, "esc")
	assert.Nil(t, m.modal)
	assert.Equal(t, 2, m.nav.depth(), "esc closed the form and popped the screen too")
}

// The help bar is the modal's while it is up.
func TestTheHelpBarBelongsToTheModal(t *testing.T) {
	m, _ := openEdit(t, testFolderToEdit())

	rendered := screen(m)
	assert.Contains(t, rendered, "enter save")
	assert.Contains(t, rendered, "esc cancel")
	assert.NotContains(t, rendered, "archived")
}

// --- Renaming ---

func TestRenamingAFolder(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	for _, key := range strings.Split(" HQ", "") {
		m, _ = press(t, m, key)
	}
	require.Equal(t, "Teams HQ", form.name.Value())

	m, _ = press(t, m, "enter")
	assert.True(t, form.saving)
	assert.NotNil(t, m.modal, "the form closed before the write came back")

	// The write lands, the form hands the new name to the workspace.
	m = load(t, m, folderRenamedMsg{id: 7, name: "Teams HQ"})
	assert.Nil(t, m.modal)
	assert.Equal(t, []string{"Home", "Teams HQ"}, m.nav.trail())
}

// A name nobody changed is nothing to write, so enter just closes.
func TestEnterOnAnUnchangedNameJustCloses(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	m, cmd := press(t, m, "enter")
	assert.Nil(t, m.modal)
	assert.False(t, form.saving)
	assert.Nil(t, cmd, "an unchanged name was written anyway")
}

func TestAFolderNeedsAName(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	for range len("Teams") {
		m, _ = press(t, m, "backspace")
	}
	require.Equal(t, "", form.name.Value())

	m, _ = press(t, m, "enter")
	assert.NotNil(t, m.modal, "an empty name closed the form")
	assert.Contains(t, ansi.Strip(form.View()), "A folder needs a name.")
}

// A rename that failed says so and leaves the form open, so the name typed into
// it is not lost.
func TestAFailedRenameKeepsTheForm(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())
	m, _ = press(t, m, "x")

	m = load(t, m, folderSavedMsg{err: errors.New("no route to host")})

	assert.NotNil(t, m.modal)
	assert.False(t, form.saving)
	assert.Contains(t, form.notice, "Could not rename the folder")
	assert.Equal(t, "Teamsx", form.name.Value())
	assert.Nil(t, m.err, "a form's own failure put an error box over the screen")
}

// --- Deleting ---

// Deleting a folder cannot be undone, so one keystroke is too few for it.
func TestDeletingTakesTwoPresses(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	m, cmd := press(t, m, deleteFolderKey)
	assert.True(t, form.confirming)
	assert.Nil(t, cmd, "one press deleted the folder")
	assert.Contains(t, ansi.Strip(form.View()), "Press d again to delete")

	_, cmd = press(t, m, deleteFolderKey)
	assert.True(t, form.saving)
	assert.NotNil(t, cmd)
}

// Anything but a second d is a change of mind.
func TestAnythingElseCancelsTheDelete(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	m, _ = press(t, m, deleteFolderKey)
	require.True(t, form.confirming)

	_, _ = press(t, m, "left")
	assert.False(t, form.confirming)
	assert.NotContains(t, ansi.Strip(form.View()), "Press d again")
}

// d is a letter while a name is being typed, and the form cannot tell the two
// apart. Saving the name first settles it.
func TestDIsALetterWhileTheNameIsChanged(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	m, _ = press(t, m, "x")
	_, _ = press(t, m, deleteFolderKey)

	assert.False(t, form.confirming)
	assert.Equal(t, "Teamsxd", form.name.Value())
	assert.Contains(t, ansi.Strip(form.View()), "Press enter to save the name")
}

// A deleted folder has no screen to go back to, so the workspace goes home.
func TestADeletedFolderSendsTheReaderHome(t *testing.T) {
	m, _ := openEdit(t, testFolderToEdit())

	m = load(t, m, folderGoneMsg{name: "Teams"})

	assert.Nil(t, m.modal)
	assert.Equal(t, []string{"Home"}, m.nav.trail())
}

func TestAFailedDeleteKeepsTheForm(t *testing.T) {
	m, form := openEdit(t, testFolderToEdit())

	m = load(t, m, folderDeletedMsg{err: errors.New("no route to host")})

	assert.NotNil(t, m.modal)
	assert.Contains(t, form.notice, "Could not delete the folder")
	assert.Equal(t, []string{"Home", "Teams"}, m.nav.trail())
}

// --- Color ---

// The color is shown as a block of it rather than a word: a name makes the
// reader picture the color, the block just is it.
func TestTheColorIsShownAsItself(t *testing.T) {
	_, form := openEdit(t, testFolderToEdit())

	rendered := ansi.Strip(form.View())
	assert.Contains(t, rendered, "blue")
	assert.Contains(t, rendered, "⬤")
}

// An uncolored folder reads as white, which is Basecamp's own default.
func TestAnUncoloredFolderReadsAsWhite(t *testing.T) {
	_, form := openEdit(t, folder{id: 7, name: "Teams"})

	assert.Contains(t, ansi.Strip(form.View()), "white")
}
