package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testReplies() []reply {
	return []reply{
		{author: person{name: "Rob Z."}, body: "Sounds good.", at: testNow.Add(-2 * time.Minute)},
		{author: person{name: "Jorge L."}, body: "I'll cut the tag.", at: testNow.Add(-time.Minute)},
	}
}

func openMessageScreen(t *testing.T, width, height int) (model, *messageScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), width, height)
	post := newMessage(m.ctx, testMessages()[0])
	post.now = func() time.Time { return testNow }
	m.push(post)

	post.Update(messageRepliesMsg{replies: testReplies()})
	m.relayout()
	return m, post
}

// --- The post ---

// The subject leads and who wrote it sits under it, then the body as Markdown:
// bold reads bold and a list gets its bullets, rather than arriving as tags.
func TestMessageShowsThePost(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "Shipping Friday")
	assert.Contains(t, rendered, "Stanko K. · 3m ago")
	assert.Contains(t, rendered, "The plan is:")
	assert.Contains(t, rendered, "cut a tag")
}

// The breadcrumb carries the subject too, but a trail truncates.
func TestMessageIsCalledItsSubject(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)

	assert.Equal(t, []string{"Home", "Shipping Friday"}, m.nav.trail())
}

func TestMessageWithoutABody(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	post := newMessage(m.ctx, message{id: 1, subject: "Bare", author: person{name: "Rob Z."}})
	m.push(post)
	m.relayout()

	assert.Contains(t, ansi.Strip(post.View()), "This message has no body.")
}

// --- Replies ---

func TestMessageShowsItsReplies(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "Comments")
	assert.Contains(t, rendered, "Rob Z. · 2m ago")
	assert.Contains(t, rendered, "Sounds good.")
	assert.Contains(t, rendered, "I'll cut the tag.")
}

// A post nobody has answered is not worth a read, so the screen never asks for
// the replies — only for who the reader is, which is what says the post is theirs
// to edit and which the replies would otherwise have carried.
func TestMessageWithNoRepliesAsksForNone(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	post := newMessage(m.ctx, message{id: 1, subject: "Quiet", comments: 0})
	m.push(post)

	post.Init()
	assert.False(t, post.reading, "a post with no replies read them anyway")
	m.relayout()

	assert.Contains(t, ansi.Strip(post.View()), "No comments yet.")
}

// The count is what arrived, not what the board's row claimed: the number on a
// message is the server's from when the board was read.
func TestMessageCountsTheRepliesItGot(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := newMessage(m.ctx, message{id: 1, subject: "Shipping", comments: 9})
	post.now = func() time.Time { return testNow }
	m.push(post)
	post.Update(messageRepliesMsg{replies: testReplies()})
	m.relayout()

	assert.Contains(t, ansi.Strip(post.View()), "Sounds good.")
}

// The post is already on screen when the replies fail, so the failure costs the
// replies and nothing else.
func TestMessageKeepsThePostWhenTheRepliesFail(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := newMessage(m.ctx, testMessages()[0])
	post.now = func() time.Time { return testNow }
	m.push(post)
	post.Update(messageRepliesMsg{err: errors.New("nope")})
	m.relayout()

	rendered := ansi.Strip(post.View())
	assert.Contains(t, rendered, "Shipping Friday")
	assert.Contains(t, rendered, "Could not load the replies")
}

// --- Scrolling ---

// A message is one thing to read rather than rows to walk, so the keys scroll.
func TestMessageScrolls(t *testing.T) {
	m, post := openMessageScreen(t, 60, 6)
	assert.Equal(t, 0, post.offset)

	m, _ = press(t, m, "down")
	assert.Equal(t, 1, post.offset)

	// The bottom of the message is as far down as it goes.
	for range 40 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(post.layout())-post.height, post.offset)

	for range 40 {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, post.offset)
}

// --- What the API gives back ---

func TestToReplyFlattensAComment(t *testing.T) {
	at := testNow.Add(-time.Hour)
	answer := toReply(basecamp.Comment{
		ID:        7,
		Content:   "<div>Looks <em>right</em></div>",
		CreatedAt: at,
		AppURL:    "https://app.basecamp.com/1/buckets/2/comments/7",
		Creator:   &basecamp.Person{ID: 5, Name: "Rob Z.", Title: "Ops"},
	}, 5)

	assert.Equal(t, int64(7), answer.id)
	assert.Equal(t, "Looks *right*", answer.body)
	assert.Equal(t, "Rob Z.", answer.author.name)
	assert.Equal(t, "Ops", answer.author.title)
	assert.Equal(t, "https://app.basecamp.com/1/buckets/2/comments/7", answer.url)
	assert.True(t, answer.mine, "the reader's own comment was not marked as theirs")
	assert.True(t, answer.at.Equal(at))
}

// Somebody else's comment is not the reader's to edit or trash, which is what
// this flag decides.
func TestToReplyMarksOnlyTheReadersOwn(t *testing.T) {
	answer := toReply(basecamp.Comment{Creator: &basecamp.Person{ID: 9, Name: "Jorge L."}}, 5)
	assert.False(t, answer.mine)

	// Not knowing who the reader is marks nothing as theirs, rather than
	// everything.
	assert.False(t, toReply(basecamp.Comment{Creator: &basecamp.Person{ID: 5}}, 0).mine)
}

// --- Layout ---

func TestMessageRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96, 140} {
		_, post := openMessageScreen(t, width, 40)
		for _, line := range strings.Split(ansi.Strip(post.View()), "\n") {
			assert.LessOrEqual(t, tui.DisplayWidth(line), post.width, "at terminal width %d", width)
		}
	}
}

// --- Tabs and selection ---

// Two tabs, the way the web shows them. Comments needs no count because they are
// right underneath it.
func TestMessageHasTwoTabs(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "Comments")
	assert.Contains(t, rendered, "References")
	assert.NotContains(t, rendered, "Comments (2)", "the comments were counted")

	_, _ = press(t, m, "tab")
	assert.Equal(t, tabReferences, post.tab)
	assert.Contains(t, ansi.Strip(post.View()), "References aren't available through the API yet.")
}

// j and k walk the comments, and off the top of them back to the message itself.
func TestMessageWalksItsComments(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	assert.Equal(t, -1, post.cursor, "a comment was selected before the reader asked")

	m, _ = press(t, m, "j")
	assert.Equal(t, 0, post.cursor)
	assert.Contains(t, ansi.Strip(post.View()), selectedMarker)

	m, _ = press(t, m, "j")
	m, _ = press(t, m, "j")
	assert.Equal(t, len(testReplies())-1, post.cursor, "the cursor ran past the last comment")

	for range 4 {
		m, _ = press(t, m, "k")
	}
	assert.Equal(t, -1, post.cursor)
	assert.NotContains(t, ansi.Strip(post.View()), selectedMarker)
}

// Nothing selected has no actions: the reader is on the message itself.
func TestMessageOffersNoActionsWithNothingSelected(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)

	_, cmd := press(t, m, "enter")
	assert.Nil(t, cmd)
}

func TestMessageOpensTheActionsOnTheSelectedComment(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)
	m, _ = press(t, m, "j")

	_, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	opened, ok := cmd().(commentActionsMsg)
	require.True(t, ok, "enter opened something else")
	assert.Equal(t, "Rob Z.", opened.comment.author.name)
}

// --- Editing your own post ---

// e opens the reader's own message in the form it was written in.
func TestEOpensYourOwnMessage(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.post.author = person{id: 7, name: "Stanko K."}
	post.me = 7

	_, cmd := press(t, m, editMessageKey)
	require.NotNil(t, cmd, "e did nothing on the reader's own message")

	opened, ok := cmd().(editMessageMsg)
	require.True(t, ok, "e opened something else")
	assert.Equal(t, post.post.id, opened.message.id)
	assert.Contains(t, helpKeys(post.HelpBindings()), editMessageKey)
}

// Somebody else's message is not the reader's to change. The server would refuse
// it too — this is about not offering it.
func TestEDoesNothingOnSomebodyElsesMessage(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.post.author = person{id: 9, name: "Rob Z."}
	post.me = 7

	_, cmd := press(t, m, editMessageKey)
	assert.Nil(t, cmd)
	assert.NotContains(t, helpKeys(post.HelpBindings()), editMessageKey)
}

// Standing on a comment, e leaves it alone: enter is what offers what can be done
// to a comment.
func TestEIsForTheMessageNotTheSelectedComment(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.post.author = person{id: 7, name: "Stanko K."}
	post.me = 7

	m, _ = press(t, m, "j")
	_, cmd := press(t, m, editMessageKey)
	assert.Nil(t, cmd)
	assert.NotContains(t, helpKeys(post.HelpBindings()), editMessageKey)
}

// Who the reader is decides this, and a read that failed to say leaves nothing
// marked as theirs rather than offering an edit that will be refused.
func TestEIsOffWhenTheReaderIsUnknown(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.post.author = person{id: 7, name: "Stanko K."}
	post.me = 0

	_, cmd := press(t, m, editMessageKey)
	assert.Nil(t, cmd)
}

// --- Boosts ---

// The reactions sit under the comment they were left on, behind the initials of
// whoever left them.
func TestMessageShowsBoostsUnderAComment(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.replies[0].id = 7
	post.Update(boostsMsg{comment: 7, boosts: []boost{
		{id: 1, content: "Sounds good!", by: person{name: "Jason Fried"}},
		{id: 2, content: "🚀", by: person{name: "Rob Zolkos"}},
	}})
	m.relayout()

	rendered := ansi.Strip(post.View())
	assert.Contains(t, rendered, "JF Sounds good!")
	assert.Contains(t, rendered, "RZ 🚀")
}

// Reactions are part of reading a thread, so they are asked for as soon as the
// comments land rather than when one is selected — but only for the comments
// that say they have any, since the API lists them one recording at a time.
func TestBoostsAreReadForEveryCommentThatHasThem(t *testing.T) {
	_, post := openMessageScreen(t, 110, 40)
	post.replies[0].id, post.replies[0].boosted = 7, 2
	post.replies[1].id, post.replies[1].boosted = 8, 0

	assert.NotNil(t, post.readBoosts(), "the boosts were never asked for")

	post.Update(boostsMsg{comment: 7, boosts: []boost{{id: 1, content: "🎉"}}})
	assert.Nil(t, post.readBoosts(), "a comment's boosts were asked for twice")
}

// A comment nobody reacted to is not worth a request.
func TestACommentWithNoBoostsIsNotAsked(t *testing.T) {
	_, post := openMessageScreen(t, 110, 40)
	for index := range post.replies {
		post.replies[index].boosted = 0
	}

	assert.Nil(t, post.readBoosts())
}

// A tab is a shape standing in front of a line, not an underlined word: the open
// one has no floor so it runs into the comments under it, and the rule carries on
// past both.
func TestTheTabsAreDrawnAsTabs(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	rows := post.tabRows()
	require.Len(t, rows, 3)

	assert.Contains(t, ansi.Strip(rows[0]), "╭──────────╮")
	assert.Contains(t, ansi.Strip(rows[1]), "│ Comments │")
	assert.Contains(t, ansi.Strip(rows[2]), "╯          ╰", "the open tab kept its floor")
	assert.Contains(t, ansi.Strip(rows[2]), "┴────────────┴", "the shut tab lost its floor")
	assert.Equal(t, post.width, tui.DisplayWidth(ansi.Strip(rows[2])), "the rule stopped short of the column")
}

// The boosts react to what was said, so they line up under it rather than under
// the face beside it.
func TestBoostsLineUpWithTheBody(t *testing.T) {
	m, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}
	post.replies[0].id = 7
	post.replies[0].author.avatar = "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"
	post.placeFaces(map[string]tui.RenderedImage{
		post.replies[0].author.avatar: post.images.Render(nil, 1, avatarCols),
	})
	post.Update(boostsMsg{comment: 7, boosts: []boost{{id: 1, content: "Sounds good!", by: person{name: "Andy Smith"}}}})
	m.relayout()

	var body, box string
	for _, line := range strings.Split(ansi.Strip(post.View()), "\n") {
		if strings.Contains(line, "Sounds good.") {
			body = line
		}
		if strings.Contains(line, "╭") {
			box = line
		}
	}
	require.NotEmpty(t, body, "the comment was not drawn")
	require.NotEmpty(t, box, "the boosts were not boxed")

	// The box's own edge lines up with the body, not the text inside it: where
	// the columns start, not how many spaces precede them, since the body's row
	// has the avatar's cells in those columns and the box's has blanks.
	assert.Equal(t,
		tui.DisplayWidth(body[:strings.Index(body, "Sounds good.")]),
		tui.DisplayWidth(box[:strings.Index(box, "╭")]),
		"the box did not line up with the body")
}

// The hint sits on the tabs' own row, and goes rather than overflowing when the
// column cannot hold both.
func TestTheTabsSayHowToSwitchThem(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	assert.Contains(t, ansi.Strip(post.tabRows()[1]), "tab to switch")

	post.Resize(28, 40)
	for _, row := range post.tabRows() {
		assert.LessOrEqual(t, tui.DisplayWidth(ansi.Strip(row)), post.width)
	}
	assert.NotContains(t, ansi.Strip(post.tabRows()[1]), "tab to switch")
}

// An avatar's payload is hundreds of kilobytes, and a thread has several faces.
// They go out as one write each: concatenated into one, the terminal kept the
// first picture and dropped the rest.
func TestEachFaceIsItsOwnWrite(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}

	drawn := post.renderFaces(map[string][]byte{
		"https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar": testImageBytes(t, 40, 20),
		"https://bc3-production-assets-cdn.basecamp-static.com/1/people/b/avatar": testImageBytes(t, 40, 20),
	})

	assert.Len(t, drawn, 2, "a face was not drawn")
	for _, rendered := range drawn {
		assert.NotEmpty(t, rendered.Raw, "a face has no pixels to send")
		assert.NotEmpty(t, rendered.Content, "a face has no cells to stand in for it")
	}
}

// A face's cells name a picture the terminal must already hold, so each face's
// pixels are followed by that same face's placement — never every payload and
// then every placement, which left whichever was written last a frame early.
func TestEachFaceIsPlacedAfterItsOwnPixels(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}
	first := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"
	second := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/b/avatar"

	require.NotNil(t, post.drawFaces(map[string][]byte{
		first:  testImageBytes(t, 40, 20),
		second: testImageBytes(t, 40, 20),
	}))

	// Each placement carries one face, so a face can only go on screen behind its
	// own write.
	post.placeFaces(map[string]tui.RenderedImage{first: post.images.Render(nil, 1, avatarCols)})
	assert.NotEmpty(t, post.face(first))
	assert.Empty(t, post.face(second), "the second face went up on the first one's write")
}

// The reactions get a box so they read as reactions rather than as more of the
// comment, and it stays inside the column.
func TestBoostsAreBoxed(t *testing.T) {
	m, post := openMessageScreen(t, 96, 40)
	post.replies[0].id = 7
	post.Update(boostsMsg{comment: 7, boosts: []boost{
		{id: 1, content: "Love it!", by: person{name: "Stanko K"}},
		{id: 2, content: "That's great", by: person{name: "Bob B"}},
	}})
	m.relayout()

	// A pill each, side by side, rather than one box around the row.
	rendered := ansi.Strip(post.View())
	assert.Contains(t, rendered, "╭─────────────╮ ╭─────────────────╮")
	assert.Contains(t, rendered, "│ SK Love it! │ │ BB That's great │")
	assert.Contains(t, rendered, "╰─────────────╯ ╰─────────────────╯")

	for _, line := range strings.Split(rendered, "\n") {
		assert.LessOrEqual(t, tui.DisplayWidth(line), post.width)
	}
}

// A narrow column wraps the reactions inside the box rather than pushing it past
// the edge.
func TestABoxOfBoostsStaysInTheColumn(t *testing.T) {
	for _, width := range []int{30, 40, 60} {
		m, post := openMessageScreen(t, width, 40)
		post.replies[0].id = 7
		post.Update(boostsMsg{comment: 7, boosts: []boost{
			{id: 1, content: "Love it!", by: person{name: "Stanko K"}},
			{id: 2, content: "That's great", by: person{name: "Bob B"}},
			{id: 3, content: "Amazing!", by: person{name: "Jane H"}},
		}})
		m.relayout()

		for _, line := range strings.Split(ansi.Strip(post.View()), "\n") {
			assert.LessOrEqual(t, tui.DisplayWidth(line), post.width, "at terminal width %d", width)
		}
	}
}

// A face on its way gets a throbber in the square it will fill, so the name
// beside it does not shift left and then right again when it lands.
func TestAFaceOnItsWayTurns(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}
	source := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"
	post.replies[0].author.avatar = source
	post.facesComing = map[string]struct{}{source: {}}

	standing := post.face(source)
	require.Len(t, standing, avatarRows, "the throbber did not fill the picture's square")
	assert.Contains(t, ansi.Strip(strings.Join(standing, "")), spinnerFrames[0])
	for _, row := range standing {
		assert.Equal(t, avatarCols, tui.DisplayWidth(ansi.Strip(row)))
	}

	// It stops once the picture is there.
	post.placeFaces(map[string]tui.RenderedImage{source: post.images.Render(nil, 1, avatarCols)})
	assert.NotContains(t, ansi.Strip(strings.Join(post.face(source), "")), spinnerFrames[0])
	assert.Empty(t, post.facesComing)
}

// A read that answered nothing stops the throbber too: one that turns forever is
// worse than no picture.
func TestAFaceThatNeverArrivedStopsTurning(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	source := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"
	post.facesComing = map[string]struct{}{source: {}}

	post.facesArrived([]string{source}, nil)
	assert.Empty(t, post.facesComing)
	assert.Nil(t, post.face(source), "a picture that never arrived still took the space")
}

// A picture that did arrive keeps its throbber until the cells are on screen:
// placement happens in a command that has not run when the read answers, so
// deciding from what is drawn took the throbber from every face at once.
func TestAFaceThatArrivedKeepsTurningUntilItIsPlaced(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}
	source := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"
	post.facesComing = map[string]struct{}{source: {}}

	post.facesArrived([]string{source}, map[string][]byte{source: testImageBytes(t, 40, 20)})
	assert.Contains(t, post.facesComing, source, "the throbber stopped before the picture was drawn")

	post.placeFaces(map[string]tui.RenderedImage{source: post.images.Render(nil, 1, avatarCols)})
	assert.Empty(t, post.facesComing)
}

// A card open over the message does not eat the faces the message asked for.
//
// Both reads answered with the same message, and a modal is handed a message
// before the screen under it — so pressing i before the avatars landed lost them
// for the life of the screen: they stayed marked as coming, so the throbber kept
// turning and readFaces would not ask again.
func TestACardOverTheMessageDoesNotEatItsFaces(t *testing.T) {
	m, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}
	source := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"
	post.replies[0].author.avatar = source
	post.facesComing = map[string]struct{}{source: {}}

	m, _ = update(t, m, personCardMsg{who: person{
		id: 5, name: "Rob Z.",
		avatar: "https://bc3-production-assets-cdn.basecamp-static.com/1/people/b/avatar",
	}})
	require.NotNil(t, m.modal, "the card never opened")

	_, cmd := update(t, m, avatarsMsg{
		asked:   []string{source},
		avatars: map[string][]byte{source: testImageBytes(t, 40, 20)},
	})
	require.NotNil(t, cmd, "the card swallowed the faces the message screen asked for")

	// The picture is drawn in a command, so it is still coming until that runs.
	assert.Contains(t, post.facesComing, source)
	post.placeFaces(map[string]tui.RenderedImage{source: post.images.Render(nil, 1, avatarCols)})
	assert.Empty(t, post.facesComing)
	assert.NotEmpty(t, post.faces[source].Content)
}

// And the card still gets its own.
func TestTheCardGetsItsOwnFace(t *testing.T) {
	m, _ := openMessageScreen(t, 96, 40)
	source := "https://bc3-production-assets-cdn.basecamp-static.com/1/people/b/avatar"

	m, _ = update(t, m, personCardMsg{who: person{id: 5, name: "Rob Z.", avatar: source}})
	card, ok := m.modal.(*personCard)
	require.True(t, ok)
	card.images = drawnImage{cols: cardFaceCols, rows: avatarRows}
	card.coming = true

	_, took := card.Update(cardFaceMsg{avatar: source, data: testImageBytes(t, 40, 20)})
	assert.True(t, took)

	card.Update(cardFacePlacedMsg{rendered: card.images.Render(nil, 1, cardFaceCols)})
	assert.False(t, card.coming)
	assert.NotEmpty(t, card.face.Content)
}

// A face already on its way is not asked for again. The screen asks twice — for
// the author when it opens and for everybody when the comments land — and two
// reads over one budget lost pictures.
func TestAFaceOnItsWayIsNotAskedForAgain(t *testing.T) {
	_, post := openMessageScreen(t, 96, 40)
	post.images = drawnImage{cols: avatarCols, rows: avatarRows}
	post.post.author.avatar = "https://bc3-production-assets-cdn.basecamp-static.com/1/people/a/avatar"

	require.NotNil(t, post.readFaces(), "the author's picture was never asked for")
	assert.Contains(t, post.facesComing, post.post.author.avatar)

	assert.Nil(t, post.readFaces(), "the same picture was asked for twice")
}

// The pills run along the row and onto the next when the column runs out, rather
// than off the edge.
func TestBoostPillsWrapOntoTheNextRow(t *testing.T) {
	m, post := openMessageScreen(t, 70, 40)
	post.replies[0].id = 7
	post.Update(boostsMsg{comment: 7, boosts: []boost{
		{id: 1, content: "Love it!", by: person{name: "Stanko K"}},
		{id: 2, content: "That's great", by: person{name: "Bob B"}},
		{id: 3, content: "Amazing!", by: person{name: "Jane H"}},
	}})
	m.relayout()

	rendered := ansi.Strip(post.View())
	assert.Equal(t, 2, strings.Count(rendered, "╭─────────────╮"), "the pills did not wrap")
	for _, line := range strings.Split(rendered, "\n") {
		assert.LessOrEqual(t, tui.DisplayWidth(line), post.width)
	}
}

// b opens the boosts on the selected comment, so the commonest thing done to a
// comment is one key rather than two.
func TestBOpensTheBoosts(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.replies[0].id = 7
	post.Update(boostsMsg{comment: 7, boosts: []boost{{id: 1, content: "🎉", by: person{name: "Andy Smith"}}}})
	m, _ = press(t, m, "j")

	_, cmd := press(t, m, "b")
	require.NotNil(t, cmd, "b did nothing on a selected comment")
	opened, ok := cmd().(boostMenuMsg)
	require.True(t, ok, "b opened something else")
	assert.Equal(t, "Rob Z.", opened.comment.author.name)
	assert.Len(t, opened.boosts, 1)
}

// With nothing selected there is no comment to boost.
func TestBDoesNothingWithNothingSelected(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)

	_, cmd := press(t, m, "b")
	assert.Nil(t, cmd)
}

// i says who wrote what the reader is standing on.
func TestIOpensACardAboutTheCommentAuthor(t *testing.T) {
	m, post := openMessageScreen(t, 110, 40)
	post.replies[0].author = person{name: "Jason Fried", title: "Co-owner"}
	m, _ = press(t, m, "j")

	_, cmd := press(t, m, "i")
	require.NotNil(t, cmd)
	opened, ok := cmd().(personCardMsg)
	require.True(t, ok, "i opened something else")
	assert.Equal(t, "Jason Fried", opened.who.name)
}

// With nothing selected it says who wrote the message.
func TestIOnTheMessageOpensACardAboutItsAuthor(t *testing.T) {
	m, _ := openMessageScreen(t, 110, 40)

	_, cmd := press(t, m, "i")
	require.NotNil(t, cmd)
	opened, ok := cmd().(personCardMsg)
	require.True(t, ok, "i opened something else")
	assert.Equal(t, "Stanko K.", opened.who.name)
}

// Nobody to say anything about is not a card with nothing on it.
func TestIDoesNothingWithNobodyToShow(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	post := newMessage(m.ctx, message{id: 1, subject: "Bare"})
	m.push(post)

	assert.Nil(t, post.openCard())
}
