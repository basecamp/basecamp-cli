package workspace

import (
	"errors"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

func testComment(mine bool) reply {
	return reply{
		id: 7, author: person{name: "Jason Fried", title: "Co-owner"}, body: "Nice summary.",
		url: "https://app.basecamp.com/1/buckets/2/comments/7", mine: mine,
	}
}

func openCommentActions(t *testing.T, msg commentActionsMsg) (model, *commentActions) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 30)
	actions := newCommentActions(m.ctx, msg)
	actions.Resize(60, 12)
	return m, actions
}

// --- What is offered ---

// Anyone may bookmark a comment or link to it. Editing and trashing belong to
// whoever wrote it — the server refuses the rest, and offering what will be
// refused is worse than not offering it.
func TestSomebodyElsesCommentOffersNoEditing(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(false)})

	labels := make([]string, 0, 2)
	for _, each := range actions.actions() {
		labels = append(labels, each.label)
	}
	assert.Equal(t, []string{"Copy link", "Bookmark"}, labels)
}

func TestYourOwnCommentOffersEditingAndTrashing(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(true), mine: true})

	labels := make([]string, 0, 4)
	for _, each := range actions.actions() {
		labels = append(labels, each.label)
	}
	assert.Equal(t, []string{"Copy link", "Bookmark", "Edit", "Move to trash"}, labels)
}

// Boosting has its own key, so it is not buried in here.
func TestBoostingIsNotAnAction(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(true), mine: true})

	for _, each := range actions.actions() {
		assert.NotContains(t, each.label, "Boost")
	}
}

// --- Editing ---

// A comment cannot be emptied by editing it, which the server refuses anyway.
func TestAnEmptyEditIsRefusedBeforeItIsSent(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(true), mine: true})
	actions.mode = commentModeEdit
	actions.body.SetValue("   ")

	cmd, stays := actions.HandleKey(keyPress("enter"))
	assert.Nil(t, cmd)
	assert.True(t, stays)
	assert.False(t, actions.saving)
	assert.Contains(t, actions.notice, "cannot be empty")
}

// The form opens with what is there, so an edit is a change rather than a
// retype.
func TestEditingOpensWithTheComment(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(true), mine: true})

	assert.Equal(t, "Nice summary.", actions.body.Value())
}

// Trashing asks first, since it is the one action that takes something away.
func TestTrashingAsksFirst(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(true), mine: true})
	actions.mode = commentModeTrash

	shown := ansi.Strip(actions.View())
	assert.Contains(t, shown, "goes to the trash")
	assert.Contains(t, shown, "esc to keep it")
}

// --- Failures ---

// A write that failed keeps the modal open with what the reader typed still in
// it, rather than closing over the loss.
func TestAFailedWriteKeepsTheForm(t *testing.T) {
	_, actions := openCommentActions(t, commentActionsMsg{comment: testComment(true), mine: true})
	actions.mode = commentModeEdit
	actions.body.SetValue("Changed my mind.")
	actions.saving = true

	_, took := actions.Update(commentChangedMsg{err: errors.New("nope")})
	assert.True(t, took)
	assert.False(t, actions.saving)
	assert.Contains(t, actions.notice, "That did not work")
	assert.Equal(t, "Changed my mind.", actions.body.Value())
}

func TestFirstName(t *testing.T) {
	assert.Equal(t, "Jason", firstName("Jason Fried"))
	assert.Equal(t, "Stanko", firstName("Stanko"))
	assert.Equal(t, "this", firstName(""))
}
