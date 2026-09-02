package workspace

import (
	"errors"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func testBoosts() []boost {
	return []boost{
		{id: 1, content: "Sounds good!", by: person{name: "Andy Smith", title: "Designer"}},
		{id: 2, content: "🚀", by: person{name: "Stanko Krtalić Rusendić"}, mine: true},
		{id: 3, content: "❤️", by: person{name: "Jay Ohms"}},
	}
}

func openBoostMenu(t *testing.T, boosts []boost) (model, *boostMenu) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 30)
	menu := newBoostMenu(m.ctx, boostMenuMsg{comment: testComment(false), boosts: boosts})
	menu.Resize(60, 12)
	return m, menu
}

// --- What it shows ---

// Everybody's, with what was left, then a quiet dot, then whoever left it in
// full: the comment has room for initials only, so this is where "who is AS"
// gets answered.
func TestTheBoostMenuNamesEverybody(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())

	shown := ansi.Strip(menu.View())
	assert.Contains(t, shown, "Sounds good! · Andy Smith")
	assert.Contains(t, shown, "🚀 · Stanko Krtalić Rusendić")
	assert.Contains(t, shown, "❤️ · Jay Ohms")
	assert.NotContains(t, shown, "AS ", "the menu fell back to initials")
}

// The reader's own is marked, since it is the only one they may take back.
func TestTheBoostMenuMarksYourOwn(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())

	assert.Contains(t, ansi.Strip(menu.View()), "yours")
}

// Leaving one is a key, not a row: a list of reactions with a button on the end
// of it makes the button look like another reaction.
func TestTheBoostMenuHasNoButtonForLeavingOne(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())

	assert.NotContains(t, ansi.Strip(menu.View()), "Leave a boost")
	assert.Contains(t, menu.HelpBindings(), helpBinding{"a", "add a boost"})
}

func TestAAddsABoost(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())

	cmd, stays := menu.HandleKey(keyPress("a"))
	assert.True(t, stays)
	assert.True(t, menu.typing)
	assert.NotNil(t, cmd, "the field never took the keys")
}

// An empty menu is not worth showing, so a comment nobody has reacted to opens
// straight on leaving the first one.
func TestACommentWithNoBoostsOpensStraightOnLeavingOne(t *testing.T) {
	_, menu := openBoostMenu(t, nil)

	assert.True(t, menu.typing)
	assert.True(t, menu.CapturingInput(), "the field did not have the keys")
	assert.Contains(t, ansi.Strip(menu.View()), "Leave a boost — a word or an emoji.")
}

// --- Taking one back ---

// Nothing here removes anything on one key press. Enter over a list is how a
// reader looks at something, and it took a boost away the first time somebody
// tried it.
func TestTakingBackABoostAsksFirst(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())
	menu.cursor = 1

	cmd, stays := menu.HandleKey(keyPress("enter"))
	assert.Nil(t, cmd, "the boost went without being confirmed")
	assert.True(t, stays)
	assert.False(t, menu.saving)
	require.NotNil(t, menu.taking, "nothing was queued for confirming")

	shown := ansi.Strip(menu.View())
	assert.Contains(t, shown, "Take back 🚀?")
	assert.Contains(t, shown, "esc to keep it")

	cmd, stays = menu.HandleKey(keyPress("enter"))
	require.NotNil(t, cmd, "the confirmed boost was not taken back")
	assert.True(t, stays)
	assert.True(t, menu.saving)
}

// Esc from the confirmation keeps the boost and the menu.
func TestEscKeepsABoost(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())
	menu.cursor = 1
	menu.HandleKey(keyPress("enter"))
	require.NotNil(t, menu.taking)

	_, stays := menu.HandleKey(keyPress("esc"))
	assert.True(t, stays, "the menu closed instead of going back to the list")
	assert.Nil(t, menu.taking)
	assert.False(t, menu.saving)
}

// Only the reader's own is theirs to remove, and saying whose it is beats a key
// that silently does nothing.
func TestTakingBackSomebodyElsesBoostIsRefused(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())
	menu.cursor = 0

	cmd, stays := menu.HandleKey(keyPress("enter"))
	assert.Nil(t, cmd, "somebody else's boost was taken back")
	assert.True(t, stays)
	assert.Nil(t, menu.taking)
	assert.Contains(t, menu.notice, "not yours to take back")
}

// --- Who left it ---

func TestIOpensACardAboutWhoLeftIt(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())
	menu.cursor = 0

	cmd, stays := menu.HandleKey(keyPress("i"))
	assert.False(t, stays, "the menu stayed open over the card")
	require.NotNil(t, cmd)

	opened, ok := cmd().(personCardMsg)
	require.True(t, ok, "i opened something else")
	assert.Equal(t, "Andy Smith", opened.who.name)
	assert.Equal(t, "Designer", opened.who.title)
}

// --- Leaving one ---

// An empty field means the reaction the web offers by default rather than
// nothing at all.
func TestBoostingWithNothingTypedUsesTheDefault(t *testing.T) {
	_, menu := openBoostMenu(t, nil)
	require.True(t, menu.typing)

	cmd, stays := menu.HandleKey(keyPress("enter"))
	assert.True(t, stays)
	assert.True(t, menu.saving)
	assert.NotNil(t, cmd, "nothing was sent")
}

// Esc from the field goes back to the list when there is one, and shuts the menu
// when there is not.
func TestEscFromTheFieldGoesBackToTheList(t *testing.T) {
	_, menu := openBoostMenu(t, testBoosts())
	menu.HandleKey(keyPress("a"))
	require.True(t, menu.typing)

	_, stays := menu.HandleKey(keyPress("esc"))
	assert.True(t, stays, "the menu closed instead of going back to the list")
	assert.False(t, menu.typing)

	empty := newBoostMenu(menu.ctx, boostMenuMsg{comment: testComment(false)})
	_, stays = empty.HandleKey(keyPress("esc"))
	assert.False(t, stays, "an empty menu had a list to go back to")
}

// --- Failures ---

func TestAFailedBoostKeepsTheField(t *testing.T) {
	_, menu := openBoostMenu(t, nil)
	menu.adding.SetValue("🚀")
	menu.saving = true

	_, took := menu.Update(commentChangedMsg{err: errors.New("nope")})
	assert.True(t, took)
	assert.False(t, menu.saving)
	assert.Contains(t, menu.notice, "That did not work")
	assert.Equal(t, "🚀", menu.adding.Value())
}

func TestToBoostMarksTheReadersOwn(t *testing.T) {
	left := toBoost(basecamp.Boost{
		ID: 3, Content: "🚀", Booster: &basecamp.Person{ID: 5, Name: "Rob Z."},
	}, 5)

	assert.Equal(t, int64(3), left.id)
	assert.Equal(t, "🚀", left.content)
	assert.Equal(t, "Rob Z.", left.by.name)
	assert.True(t, left.mine)

	assert.False(t, toBoost(basecamp.Boost{Booster: &basecamp.Person{ID: 9}}, 5).mine)
	assert.False(t, toBoost(basecamp.Boost{Booster: &basecamp.Person{ID: 5}}, 0).mine,
		"not knowing the reader marked a boost as theirs")
}
