package workspace

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

func testPerson() person {
	return person{
		id: 5, name: "Stanko Krtalić Rusendić", title: "Senior Programmer, Product",
		company: "37signals", location: "Zagreb, Croatia", timeZone: "Europe/Zagreb",
		bio: "🌟 OUT on Fridays", email: "stanko@example.com",
		avatar: "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar",
	}
}

// --- Who somebody is ---

func TestToPersonFlattensWhatTheAPISends(t *testing.T) {
	who := toPerson(&basecamp.Person{
		ID: 5, Name: "Stanko K", Title: "Programmer", Bio: "Out on Fridays",
		Location: "Zagreb", TimeZone: "Europe/Zagreb", EmailAddress: "s@example.com",
		AvatarURL: "https://example.com/a", Company: &basecamp.PersonCompany{Name: "37signals"},
	})

	assert.Equal(t, int64(5), who.id)
	assert.Equal(t, "Stanko K", who.name)
	assert.Equal(t, "Out on Fridays", who.bio)
	assert.Equal(t, "37signals", who.company)
	assert.True(t, who.known())

	assert.False(t, toPerson(nil).known())
}

// BC5 renamed the bio to a tagline and sends both; an older response sends only
// the one.
func TestToPersonFallsBackToTheTagline(t *testing.T) {
	who := toPerson(&basecamp.Person{Name: "Rob Z.", Tagline: "Ops"})

	assert.Equal(t, "Ops", who.bio)
}

// Two halves of the same answer, and most people have only the first.
func TestWhereSomebodyWorks(t *testing.T) {
	assert.Equal(t, "Programmer at 37signals", person{title: "Programmer", company: "37signals"}.where())
	assert.Equal(t, "Programmer", person{title: "Programmer"}.where())
	assert.Empty(t, person{}.where())
}

// What time it is for somebody is the thing a card about a colleague in another
// country is really for.
func TestWhatTimeItIsForSomebody(t *testing.T) {
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	assert.Equal(t, "14:00 CEST", person{timeZone: "Europe/Zagreb"}.clock(at))
	assert.Equal(t, "08:00 EDT", person{timeZone: "America/New_York"}.clock(at))

	// A wrong time is worse than none, so an unknown zone says nothing.
	assert.Empty(t, person{}.clock(at))
	assert.Empty(t, person{timeZone: "Mars/Olympus"}.clock(at))
}

func TestPersonInitials(t *testing.T) {
	assert.Equal(t, "JF", person{name: "Jason Fried"}.initials())
	assert.Equal(t, "S", person{name: "Stanko"}.initials())
	assert.Equal(t, "SK", person{name: "Stanko Krtalić Rusendić"}.initials())
	assert.Equal(t, "??", person{}.initials())
	assert.Equal(t, "??", person{name: "🎉"}.initials(), "an emoji is not a letter")
}

// --- The card ---

func openPersonCard(t *testing.T, who person) (model, *personCard) {
	t.Helper()

	m := resize(t, newTestModel(t), 96, 30)
	card := newPersonCard(m.ctx, who)
	card.now = func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }
	card.Resize(40, 20)
	return m, card
}

func TestThePersonCardSaysWhoSomebodyIs(t *testing.T) {
	_, card := openPersonCard(t, testPerson())

	// The name is the frame's title rather than a row of its own, so it is not
	// on the card twice.
	assert.Equal(t, "Stanko Krtalić Rusendić", card.Title())

	shown := ansi.Strip(card.View())
	assert.NotContains(t, shown, "Stanko Krtalić Rusendić")
	assert.Contains(t, shown, "Senior Programmer, Product at 37signals")
	assert.Contains(t, shown, "Zagreb, Croatia · 14:00 CEST")
	assert.Contains(t, shown, "🌟 OUT on Fridays")
	assert.Contains(t, shown, "stanko@example.com")
}

// Somebody who has said less about themselves gets a shorter card, not a card
// with gaps in it.
func TestThePersonCardLeavesOutWhatIsNotThere(t *testing.T) {
	_, card := openPersonCard(t, person{name: "Rob Z."})

	assert.Equal(t, "Rob Z.", card.Title())
	assert.Empty(t, ansi.Strip(card.View()))
}

// A card is a name and three short lines, so it asks for the room those need
// rather than the share of the terminal every modal may take.
func TestThePersonCardAsksForLessRoom(t *testing.T) {
	m := resize(t, newTestModel(t), 140, 40)
	card := newPersonCard(m.ctx, testPerson())
	card.now = func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

	assert.Less(t, modalWidthFor(card, m.width), modalWidth(m.width),
		"the card took the whole share a modal may have")
	assert.Equal(t, card.Widest()+modalChromeWidth, modalWidthFor(card, m.width))

	// The share is still the ceiling: a card asks for less, never for more.
	long := newPersonCard(m.ctx, person{name: strings.Repeat("a very long name ", 20)})
	assert.Equal(t, modalWidth(m.width), modalWidthFor(long, m.width))
}

// A card is something to read, so anything that means "done" closes it.
func TestThePersonCardClosesOnAnythingThatMeansDone(t *testing.T) {
	_, card := openPersonCard(t, testPerson())

	for _, key := range []string{"esc", "enter", "q", "i"} {
		_, stays := card.HandleKey(keyPress(key))
		assert.False(t, stays, key)
	}

	_, stays := card.HandleKey(keyPress("j"))
	assert.True(t, stays, "a key that means nothing here closed the card")
}

// The picture goes out as pixels and comes back as cells, the same two steps as
// everywhere else one is drawn.
func TestThePersonCardDrawsAFace(t *testing.T) {
	_, card := openPersonCard(t, testPerson())
	card.images = drawnImage{cols: cardFaceCols, rows: 8}
	card.coming = true

	cmd, took := card.Update(avatarsMsg{
		asked:   []string{testPerson().avatar},
		avatars: map[string][]byte{testPerson().avatar: testImageBytes(t, 200, 200)},
	})
	require.True(t, took)
	require.NotNil(t, cmd, "the pixels were never sent")
	assert.True(t, card.coming, "the cells went up in the same frame as the pixels")

	card.Update(cardFacePlacedMsg{rendered: card.images.Render(nil, 1, cardFaceCols)})
	assert.False(t, card.coming)
	assert.Contains(t, ansi.Strip(card.View()), "▒")
}

// A read that answered nothing stops the throbber rather than turning forever.
func TestThePersonCardStopsWaitingOnAFailedRead(t *testing.T) {
	_, card := openPersonCard(t, testPerson())
	card.coming = true

	card.Update(avatarsMsg{asked: []string{testPerson().avatar}})
	assert.False(t, card.coming)
	assert.NotContains(t, ansi.Strip(card.View()), "Loading")
}
