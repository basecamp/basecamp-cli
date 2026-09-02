package workspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCategories() []messageCategory {
	return []messageCategory{
		{id: 1, name: "Announcement", icon: "📣"},
		{id: 2, name: "FYI", icon: "💡"},
	}
}

func openMessageForm(t *testing.T, categories []messageCategory) (model, *messageForm) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 30)
	form := newMessageForm(m.ctx, newMessageMsg{board: testMessageBoardTool(), bucket: 48521764})
	form.Resize(60, 24)
	form.Update(messageCategoriesMsg{categories: categories})
	return m, form
}

// --- Opening it ---

// a on the board opens the form, which is where a new message is written.
func TestAOpensTheMessageForm(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)

	_, cmd := press(t, m, "a")
	require.NotNil(t, cmd, "a did nothing on the board")

	opened, ok := cmd().(newMessageMsg)
	require.True(t, ok, "a opened something else")
	assert.Equal(t, testMessageBoardTool().id, opened.board.id)
	assert.Equal(t, int64(48521764), opened.bucket, "the form has no project to read categories from")
}

// The form is a screen, not a modal: a body worth typing wants the whole
// terminal, and writing a message is what the reader is doing rather than
// something they are doing over the board.
func TestTheFormIsAScreen(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)
	depth := m.nav.depth()

	m2, _ := update(t, m, newMessageMsg{board: testMessageBoardTool(), bucket: 48521764})
	assert.Nil(t, m2.modal, "the form opened as a modal")
	assert.Equal(t, depth+1, m2.nav.depth(), "the form did not go on the stack")
	assert.IsType(t, &messageForm{}, m2.nav.current())
}

// --- The fields ---

// A title, a body, and a category when the project has any. Tab walks them.
func TestTheFormWalksItsFields(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	assert.Equal(t, messageFieldSubject, form.on, "the form opened on something other than the title")

	form.HandleKey(keyPress("tab"))
	assert.Equal(t, messageFieldBody, form.on)

	form.HandleKey(keyPress("shift+tab"))
	assert.Equal(t, messageFieldSubject, form.on)

	form.HandleKey(keyPress("shift+tab"))
	assert.Equal(t, messageFieldCategory, form.on)
}

// A project with no categories gets a form without that row, rather than a
// picker with nothing in it.
func TestAProjectWithNoCategoriesHasNoCategoryRow(t *testing.T) {
	_, form := openMessageForm(t, nil)

	assert.NotContains(t, ansi.Strip(form.View()), "Category")

	// And tab never lands on it.
	form.HandleKey(keyPress("shift+tab"))
	assert.Equal(t, messageFieldSubject, form.on)
}

// The picker shows one category at a time. A project can have a dozen, and a row
// of a dozen is wider than the form.
func TestTheCategoryPickerShowsOneAtATime(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.on = messageFieldCategory

	shown := ansi.Strip(form.View())
	assert.Contains(t, shown, "No category")
	assert.NotContains(t, shown, "Announcement", "every category is on the row at once")

	form.HandleKey(keyPress("right"))
	shown = ansi.Strip(form.View())
	assert.Contains(t, shown, "📣 Announcement")
	assert.NotContains(t, shown, "No category")
	assert.NotContains(t, shown, "FYI", "the next category is on the row too")
}

// The chevrons say which way there is another one to reach, and hold their
// columns when there isn't so the label stays put.
func TestTheCategoryPickerPointsBothWays(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.on = messageFieldCategory

	assert.Equal(t, "  No category ›", ansi.Strip(form.categoryRow()), "nothing to the left of no category")

	form.HandleKey(keyPress("right"))
	assert.Equal(t, "‹ 📣 Announcement ›", ansi.Strip(form.categoryRow()))

	form.HandleKey(keyPress("right"))
	assert.Equal(t, "‹ 💡 FYI  ", ansi.Strip(form.categoryRow()), "nothing to the right of the last one")
}

// A category is optional, so "none" is the first stop and stays reachable after
// picking one.
func TestACategoryIsOptional(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.on = messageFieldCategory
	assert.Zero(t, form.categoryID())

	form.HandleKey(keyPress("right"))
	assert.Equal(t, int64(1), form.categoryID())

	form.HandleKey(keyPress("right"))
	assert.Equal(t, int64(2), form.categoryID())

	// It stops at the last one rather than running off the end.
	form.HandleKey(keyPress("right"))
	assert.Equal(t, int64(2), form.categoryID())

	for range 4 {
		form.HandleKey(keyPress("left"))
	}
	assert.Zero(t, form.categoryID(), "no category was not reachable again")
}

// The body is the same composer a chat message is written in, so the Markdown
// reads the way it will arrive.
func TestTheBodyPreviewsItsMarkdown(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.on = messageFieldBody
	form.body.SetValue("The **plan** is a `tag`")

	rows := form.body.rows()
	require.NotEmpty(t, rows)
	assert.NotEqual(t, rows[0], ansi.Strip(rows[0]), "the body drew its Markdown unstyled")
}

// A form is somewhere a reader types, so every key goes to it — the section
// digits and the menu chord included.
func TestTheFormKeepsEveryKey(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	assert.True(t, form.CapturingInput())

	form.on = messageFieldCategory
	assert.True(t, form.CapturingInput(), "a digit typed on the category row would jump to a section")
}

// --- Saving ---

// A title is the one thing the API insists on, so it is checked here rather than
// sent to be refused.
func TestAMessageNeedsATitle(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.body.SetValue("Words without a title.")

	cmd := form.HandleKey(keyPress(postMessageKey))
	assert.False(t, form.saving)
	assert.Contains(t, form.notice, "needs a title")
	assert.Equal(t, messageFieldSubject, form.on, "the cursor was left where the title was not")
	assert.NotNil(t, cmd, "the title field did not take the keys back")
}

// The two ways of saving work from anywhere in the form: a reader who has just
// typed the last word of the body should not have to walk back to a button.
func TestAMessageIsPostedOrDrafted(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.subject.SetValue("Shipping Friday")
	form.on = messageFieldBody

	require.NotNil(t, form.HandleKey(keyPress(postMessageKey)))
	assert.True(t, form.saving, "posting from the body did nothing")

	_, draft := openMessageForm(t, testCategories())
	draft.subject.SetValue("Shipping Friday")
	draft.on = messageFieldBody

	require.NotNil(t, draft.HandleKey(keyPress(draftMessageKey)))
	assert.True(t, draft.saving, "saving a draft from the body did nothing")
}

// What it was saved as is what the toast says. A draft nobody else can see is
// easy to mistake for a post, and a change to something already read is not a
// posting.
func TestSayingWhichWayItWasSaved(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	saved := message{id: 10, subject: "Shipping Friday"}

	form.saving = true
	cmd, took := form.Update(messageWrittenMsg{saved: saved})
	require.True(t, took)
	require.NotNil(t, cmd)
	assert.Equal(t, "Posted Shipping Friday", cmd().(messageSavedMsg).said)

	form.saving = true
	cmd, _ = form.Update(messageWrittenMsg{saved: saved, draft: true})
	require.NotNil(t, cmd)
	assert.Equal(t, "Saved Shipping Friday as a draft", cmd().(messageSavedMsg).said)

	form.saving = true
	cmd, _ = form.Update(messageWrittenMsg{saved: saved, wasPosted: true})
	require.NotNil(t, cmd)
	assert.Equal(t, "Saved Shipping Friday", cmd().(messageSavedMsg).said)
}

// A message that saved closes the form and hands the board back, reading again so
// the new message is there rather than appearing on the next visit.
func TestSavingClosesTheFormAndRereadsTheBoard(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)
	m, _ = update(t, m, newMessageMsg{board: testMessageBoardTool(), bucket: 48521764})
	require.IsType(t, &messageForm{}, m.nav.current())

	m, cmd := update(t, m, messageSavedMsg{said: "Posted Shipping Friday"})
	assert.IsType(t, &messageBoardScreen{}, m.nav.current(), "the form stayed open")
	require.NotNil(t, cmd)
}

// --- Editing ---

func testMyPost() message {
	return message{
		id: 10, subject: "Shipping Friday", body: "The plan is:",
		author: person{id: 7, name: "Stanko K."}, bucket: 48521764, categoryID: 2,
	}
}

// The form over a message already written holds what the message holds, so the
// reader changes it rather than retyping it.
func TestEditingHoldsWhatTheMessageHolds(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	form := newMessageEdit(m.ctx, testMyPost())
	form.Resize(60, 24)

	assert.Equal(t, "Edit message", form.Title())
	assert.Equal(t, "Shipping Friday", form.subject.Value())
	assert.Equal(t, "The plan is:", form.body.Value())

	// The category it already has is what the picker stands on once the list of
	// them lands.
	form.Update(messageCategoriesMsg{categories: testCategories()})
	assert.Equal(t, int64(2), form.categoryID())
	assert.Contains(t, ansi.Strip(form.categoryRow()), "💡 FYI")
}

// A category the project no longer offers leaves the picker on none, which is
// what saving it would do anyway.
func TestEditingAMessageWhoseCategoryIsGone(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := testMyPost()
	post.categoryID = 999
	form := newMessageEdit(m.ctx, post)

	form.Update(messageCategoriesMsg{categories: testCategories()})
	assert.Zero(t, form.categoryID())
}

// An edit that changed nothing closes on esc. There is nothing to lose, so there
// is nothing to ask about.
func TestEscOnAnUnchangedEditJustCloses(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	form := newMessageEdit(m.ctx, testMyPost())
	form.Update(messageCategoriesMsg{categories: testCategories()})

	cmd := form.HandleKey(keyPress("esc"))
	require.NotNil(t, cmd)
	assert.IsType(t, closeScreenMsg{}, cmd())

	// Change one word and it asks.
	form.body.SetValue("The plan changed:")
	assert.Nil(t, form.HandleKey(keyPress("esc")))
	assert.True(t, form.leaving)
}

// Changing only the category counts as a change.
func TestChangingOnlyTheCategoryCounts(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := testMyPost()
	post.categoryID = 1
	form := newMessageEdit(m.ctx, post)
	form.Update(messageCategoriesMsg{categories: testCategories()})
	form.on = messageFieldCategory

	form.HandleKey(keyPress("right"))
	assert.Equal(t, int64(2), form.categoryID())
	assert.Nil(t, form.HandleKey(keyPress("esc")))
	assert.True(t, form.leaving)
}

// A category can be swapped but not removed: a zero id never reaches the wire, and
// the server reads a missing category_id on an update as "leave it alone". The
// picker stops where the API does rather than offering a change that gets dropped.
func TestEditingCannotClearACategory(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	form := newMessageEdit(m.ctx, testMyPost())
	form.Update(messageCategoriesMsg{categories: testCategories()})
	form.on = messageFieldCategory
	require.Equal(t, int64(2), form.categoryID())

	form.HandleKey(keyPress("left"))
	assert.Equal(t, int64(1), form.categoryID(), "the picker did not reach the other category")

	form.HandleKey(keyPress("left"))
	assert.Equal(t, int64(1), form.categoryID(), "the picker cleared a category the API cannot clear")
	assert.Contains(t, form.notice, "isn't possible through the API yet")
	assert.NotContains(t, ansi.Strip(form.categoryRow()), "‹", "the chevron pointed somewhere there is nothing")
}

// A message being written can still have no category: create leaves the key off
// and the server reads that as none, which is what it means.
func TestANewMessageCanHaveNoCategory(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.on = messageFieldCategory

	form.HandleKey(keyPress("right"))
	require.Equal(t, int64(1), form.categoryID())

	form.HandleKey(keyPress("left"))
	assert.Zero(t, form.categoryID())
	assert.Empty(t, form.notice)
}

// A message people have already read does not go back to being unwritten, so
// saving it as a draft is not on offer.
func TestAPostedMessageHasOneWayToSave(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	form := newMessageEdit(m.ctx, testMyPost())

	keys := ansi.Strip(strings.Join(helpKeys(form.HelpBindings()), " "))
	assert.Contains(t, keys, postMessageKey)
	assert.NotContains(t, keys, draftMessageKey)

	assert.Nil(t, form.HandleKey(keyPress(draftMessageKey)), "a posted message offered to become a draft")
	assert.False(t, form.saving)
}

// A draft still has both: post it, or keep it to yourself a while longer.
func TestADraftBeingEditedKeepsBothWaysToSave(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := testMyPost()
	post.draft = true
	form := newMessageEdit(m.ctx, post)

	assert.Equal(t, "Continue writing", form.Title())

	keys := strings.Join(helpKeys(form.HelpBindings()), " ")
	assert.Contains(t, keys, postMessageKey)
	assert.Contains(t, keys, draftMessageKey)

	require.NotNil(t, form.HandleKey(keyPress(draftMessageKey)))
	assert.True(t, form.saving)
}

// The screen showing the very message that was edited takes the new one directly:
// the server has just handed it over, so there is nothing to ask for.
func TestTheMessageScreenTakesTheEditedMessage(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)
	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)
	require.IsType(t, &messageScreen{}, m.nav.current())

	m, _ = update(t, m, editMessageMsg{message: testMyPost()})
	require.IsType(t, &messageForm{}, m.nav.current())

	changed := testMyPost()
	changed.subject, changed.body = "Shipping Monday", "It slipped."
	m, _ = update(t, m, messageSavedMsg{said: "Saved Shipping Monday", saved: changed})

	post, ok := m.nav.current().(*messageScreen)
	require.True(t, ok, "the form did not hand the message screen back")
	assert.Equal(t, "Shipping Monday", post.post.subject)
	assert.Equal(t, "It slipped.", post.post.body)
	assert.Equal(t, "Shipping Monday", post.Title(), "the trail still says the old title")
}

// A write that failed keeps the form and everything typed into it.
func TestAFailedPostKeepsTheForm(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.subject.SetValue("Shipping Friday")
	form.body.SetValue("The plan is:")
	form.saving = true

	_, took := form.Update(messageWrittenMsg{err: errors.New("nope")})
	assert.True(t, took)
	assert.False(t, form.saving)
	assert.Contains(t, form.notice, "Could not save the message")
	assert.Equal(t, "Shipping Friday", form.subject.Value())
	assert.Equal(t, "The plan is:", form.body.Value())
}

// Categories that could not be read leave a form without that row rather than
// stopping a reader from writing anything at all.
func TestCategoriesThatCouldNotBeReadAreSkipped(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	form := newMessageForm(m.ctx, newMessageMsg{board: testMessageBoardTool(), bucket: 1})
	form.Resize(60, 24)

	form.Update(messageCategoriesMsg{err: errors.New("nope")})
	assert.Empty(t, form.categories)
	assert.NotContains(t, ansi.Strip(form.View()), "Category")
	assert.Empty(t, form.notice, "a form that can still be used said something went wrong")
}

// --- Leaving ---

// Esc on an untouched form closes it: there is nothing to lose.
func TestEscClosesAnEmptyForm(t *testing.T) {
	_, form := openMessageForm(t, testCategories())

	cmd := form.HandleKey(keyPress("esc"))
	require.NotNil(t, cmd)
	assert.IsType(t, closeScreenMsg{}, cmd(), "an empty form asked before closing")
	assert.False(t, form.leaving)
}

// Esc on a written one asks first. A message half written is worth more than the
// key press it takes to confirm.
func TestEscOnAWrittenFormAsksFirst(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.body.SetValue("The plan is:")

	assert.Nil(t, form.HandleKey(keyPress("esc")), "the form closed on what was typed into it")
	assert.True(t, form.leaving)
	assert.Contains(t, ansi.Strip(form.View()), "has not been saved")

	// Esc again keeps writing, with everything still there.
	assert.Nil(t, form.HandleKey(keyPress("esc")))
	assert.False(t, form.leaving)
	assert.Equal(t, "The plan is:", form.body.Value())

	// Enter is what leaves.
	form.HandleKey(keyPress("esc"))
	cmd := form.HandleKey(keyPress("enter"))
	require.NotNil(t, cmd)
	assert.IsType(t, closeScreenMsg{}, cmd())
}

// A title on its own counts as written: it is the one thing a message needs.
func TestATitleAloneCountsAsWritten(t *testing.T) {
	_, form := openMessageForm(t, testCategories())
	form.subject.SetValue("Shipping Friday")

	assert.Nil(t, form.HandleKey(keyPress("esc")))
	assert.True(t, form.leaving)
}
