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

func testMessageBoardTool() tool {
	return tool{id: 1, kind: messageBoardKind, name: "Message Board"}
}

func testMessages() []message {
	return []message{
		{id: 10, subject: "Shipping Friday", body: "The **plan** is:\n\n- cut a tag\n- tell everyone",
			author: person{name: "Stanko K."}, comments: 3, at: testNow.Add(-3 * time.Minute)},
		{id: 11, subject: "Welcome aboard", body: "Glad you're here.",
			author: person{name: "Rob Z."}, kind: "Announcement", comments: 0, at: testNow.Add(-10 * time.Hour)},
	}
}

func openMessageBoard(t *testing.T, width, height int) (model, *messageBoardScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), width, height)
	b := newMessageBoard(m.ctx, testMessageBoardTool(), project{id: 48521764, name: "CLIs"})
	b.now = func() time.Time { return testNow }
	m.push(b)

	b.Update(messagePageMsg{page: 1, messages: testMessages()})
	b.done, b.paging = true, false
	m.relayout()
	return m, b
}

// --- The board ---

// A post is two lines: what it is called, then who wrote it and how many have
// answered, with the time down the same column the feeds use.
func TestMessageBoardListsItsPosts(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)
	rendered := ansi.Strip(screen(m))

	assert.Contains(t, rendered, "3m ago Shipping Friday")
	assert.Contains(t, rendered, "Stanko K. · 3 replies")
	assert.Contains(t, rendered, "10h ago Welcome aboard")
}

// A board's categories are what the web shows on the card, so they belong on the
// row that stands for it.
func TestMessageBoardShowsACategory(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)

	assert.Contains(t, ansi.Strip(screen(m)), "Rob Z. · Announcement")
}

// One reply is a reply, not 1 replies. Nobody having answered says nothing at
// all rather than "0 replies".
func TestMessageBoardCountsReplies(t *testing.T) {
	assert.Empty(t, message{comments: 0}.replies())
	assert.Equal(t, "1 reply", message{comments: 1}.replies())
	assert.Equal(t, "4 replies", message{comments: 4}.replies())
}

func TestMessageBoardWithNothingPosted(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	b := newMessageBoard(m.ctx, testMessageBoardTool(), project{id: 48521764, name: "CLIs"})
	m.push(b)
	b.Update(messagePageMsg{page: 1})
	m.relayout()

	assert.Contains(t, ansi.Strip(b.View()), "Nothing posted yet.")
}

// A page that never arrived leaves what is on screen alone and offers another
// go, rather than reading as the end of the board.
func TestMessageBoardStallsRatherThanEnding(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	b.done, b.paging = false, false
	b.Update(messagePageMsg{page: 2, err: errors.New("nope")})
	m.relayout()

	rendered := ansi.Strip(b.View())
	assert.Contains(t, rendered, "Shipping Friday", "the posts already read were dropped")
	assert.Contains(t, rendered, "Could not load more. Press ↓ to try again.")
}

// Nothing read and nothing arriving is the one case where the failure is all
// there is to say.
func TestMessageBoardThatCouldNotBeRead(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 26)
	b := newMessageBoard(m.ctx, testMessageBoardTool(), project{id: 48521764, name: "CLIs"})
	m.push(b)
	b.Update(messagePageMsg{page: 1, err: errors.New("nope")})
	m.relayout()

	assert.Contains(t, ansi.Strip(b.View()), "Could not load the messages")
}

// --- Walking and opening ---

func TestMessageBoardCursorWalksThePosts(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	assert.Equal(t, 0, b.cursor)

	m, _ = press(t, m, "down")
	assert.Equal(t, 1, b.cursor)

	// The cursor stops at the last post rather than running off the end.
	for range 10 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(testMessages())-1, b.cursor)

	for range 10 {
		m, _ = press(t, m, "up")
	}
	assert.Equal(t, 0, b.cursor)
}

// A post hangs off the board the way the board hangs off the project, so the
// trail says where the reader came from.
func TestOpeningAPostHangsOffTheBoard(t *testing.T) {
	m, _ := openMessageBoard(t, 110, 30)

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "Message Board", "Shipping Friday"}, m.nav.trail())
	post, ok := m.nav.current().(*messageScreen)
	require.True(t, ok, "the post opened something else")
	assert.Equal(t, int64(10), post.post.id)
}

// --- Drafts ---

func testDrafts() []message {
	return []message{
		{id: 20, subject: "✨ TEST", body: "THIS IS JUST A TEST", draft: true, at: testNow.Add(-time.Minute)},
	}
}

// A draft is only visible to whoever wrote it, so the board shows the reader's
// own above what is posted — the web puts them above the list too.
func TestMessageBoardShowsYourDraftsFirst(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	b.Update(messageDraftsMsg{drafts: testDrafts()})
	m.relayout()

	rendered := ansi.Strip(b.View())
	assert.Contains(t, rendered, "✨ TEST")
	assert.Contains(t, rendered, "Draft", "nothing said it was unposted")
	assert.Less(t, strings.Index(rendered, "✨ TEST"), strings.Index(rendered, "Shipping Friday"),
		"the draft went below the posted messages")
}

// The cursor walks the drafts and the posts as one list.
func TestMessageBoardCursorWalksDraftsAndPosts(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	b.Update(messageDraftsMsg{drafts: testDrafts()})

	for range 10 {
		m, _ = press(t, m, "down")
	}
	assert.Equal(t, len(testDrafts())+len(testMessages())-1, b.cursor)
}

// A draft's body has to be read before it can be opened: the drafts list carries
// a plain-text excerpt, not the message. It then opens in the form it was written
// in rather than as something to read — the web does the same, and the link to it
// says "Continue writing your draft".
func TestOpeningADraftReadsItThenOpensTheForm(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	b.Update(messageDraftsMsg{drafts: testDrafts()})
	m.relayout()

	m, cmd := press(t, m, "enter")
	require.NotNil(t, cmd)
	assert.True(t, b.opening, "the board did not say it was waiting")

	read := message{id: 20, subject: "✨ TEST", body: "The real body.", draft: true, bucket: 48521764}
	m, cmd = update(t, m, messageReadMsg{message: read})
	assert.False(t, b.opening)
	m = deliver(t, m, cmd)

	form, ok := m.nav.current().(*messageForm)
	require.True(t, ok, "the draft opened something else")
	assert.Equal(t, "The real body.", form.body.Value())
	assert.Equal(t, "✨ TEST", form.subject.Value())
	assert.Equal(t, "Continue writing", form.Title())
}

// A draft that could not be read leaves the board alone and says so, rather than
// standing on a spinner.
func TestADraftThatCouldNotBeOpened(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	b.opening = true

	_, cmd := update(t, m, messageReadMsg{err: errors.New("nope")})
	assert.False(t, b.opening)
	require.NotNil(t, cmd)
	assert.Contains(t, cmd().(notifyMsg).text, "Could not open the draft")
}

// Drafts that could not be read leave the board as it was. They are a second read
// for the reader's own unfinished writing, not the board.
func TestDraftsThatCouldNotBeReadAreSkipped(t *testing.T) {
	m, b := openMessageBoard(t, 110, 30)
	b.Update(messageDraftsMsg{err: errors.New("nope")})
	m.relayout()

	assert.Empty(t, b.drafts)
	rendered := ansi.Strip(b.View())
	assert.Contains(t, rendered, "Shipping Friday")
	assert.NotContains(t, rendered, "Could not")
}

// A draft says it is one instead of naming an author and counting replies: it is
// the reader's own, and nobody has replied to something nobody else can see.
func TestADraftsBylineSaysItIsADraft(t *testing.T) {
	assert.Equal(t, "Draft", message{draft: true, author: person{name: "Stanko K."}, comments: 3}.byline())
	assert.Equal(t, "Stanko K. · 3 replies", message{author: person{name: "Stanko K."}, comments: 3}.byline())
}

func TestToDraftFlattensWhatTheAPISends(t *testing.T) {
	at := testNow.Add(-time.Hour)
	unposted := toDraft(basecamp.Draft{
		ID:        20,
		Title:     "✨ TEST",
		Excerpt:   "THIS IS JUST A TEST",
		UpdatedAt: at,
	})

	assert.Equal(t, int64(20), unposted.id)
	assert.Equal(t, "✨ TEST", unposted.subject)
	assert.Equal(t, "THIS IS JUST A TEST", unposted.body)
	assert.True(t, unposted.draft)
	assert.Equal(t, at.Local(), unposted.at)
}

// --- What the API gives back ---

func TestToMessageFlattensWhatTheAPISends(t *testing.T) {
	at := testNow.Add(-time.Hour)
	post := toMessage(basecamp.Message{
		ID:            10,
		Subject:       "Shipping Friday",
		Content:       "<div><strong>Soon</strong></div>",
		CommentsCount: 2,
		CreatedAt:     at,
		Creator:       &basecamp.Person{Name: "Stanko K."},
		Category:      &basecamp.MessageType{Name: "Announcement"},
	})

	assert.Equal(t, int64(10), post.id)
	assert.Equal(t, "Shipping Friday", post.subject)
	assert.Equal(t, "**Soon**", post.body)
	assert.Equal(t, "Stanko K.", post.author.name)
	assert.Equal(t, "Announcement", post.kind)
	assert.Equal(t, 2, post.comments)
	assert.True(t, post.at.Equal(at))
}

// A message posted without a subject is titled by the API instead, so there is
// always something on the first line.
func TestToMessageFallsBackToTheTitle(t *testing.T) {
	post := toMessage(basecamp.Message{ID: 11, Title: "Re: Shipping", Content: "<div>ok</div>"})

	assert.Equal(t, "Re: Shipping", post.subject)
}

// --- Layout ---

func TestMessageBoardRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96, 140} {
		_, b := openMessageBoard(t, width, 26)
		for _, line := range strings.Split(ansi.Strip(b.View()), "\n") {
			assert.LessOrEqual(t, tui.DisplayWidth(line), b.width, "at terminal width %d", width)
		}
	}
}
