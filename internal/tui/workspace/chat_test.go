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

func testChatTool() tool {
	return tool{id: 10206809020, kind: chatKind, name: "Chat"}
}

func said(who string, ago time.Duration, body string) chatLine {
	return chatLine{who: who, body: body, at: testNow.Add(-ago)}
}

// testTranscript is what the newest page of a chat looks like once converted:
// oldest first, the way it is drawn.
func testTranscript() []chatLine {
	return []chatLine{
		said("David Heinemeier Hansson", 30*time.Hour, "AUR is a dead end for us"),
		said("Rob Zolkos", 90*time.Minute, "nice! I was playing with the one\nthats in the current cli"),
		said("Stanko Krtalic Rusendic", 20*time.Minute, "Just a small preview of last week's TUI experiment."),
		said("Stanko Krtalic Rusendic", 20*time.Minute, "📎 screenrecording.mp4 (2.6mb)"),
	}
}

// openChat is a chat on screen with its newest page in and the walk stopped, so
// a test is not racing a page it never asked for.
func openChat(t *testing.T, width, height int) (model, *chatScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), width, height)
	c := newChat(m.ctx, testChatTool())
	c.now = func() time.Time { return testNow }
	m.push(c)

	// The API answers newest first, which is the order the screen has to flip.
	c.Update(chatPageMsg{page: 1, lines: reversed(testTranscript())})
	stopWalking(c)
	m.relayout()
	return m, c
}

// stopWalking pretends the conversation has no more pages, so a test is not racing
// a read it never asked for. A page arriving asks for the one above it, so the
// request in flight is cleared too.
func stopWalking(c *chatScreen) {
	c.done = true
	c.paging = false
}

// --- What a line says ---

// A line is rich text on the wire. What a terminal wants is the text.
func TestAChatLineIsReadAsText(t *testing.T) {
	line := toChatLine(basecamp.CampfireLine{
		ID:        1,
		Content:   `<p dir="auto">AUR is a dead end for us</p>`,
		Creator:   &basecamp.Person{Name: "David Heinemeier Hansson"},
		CreatedAt: testNow,
	})

	assert.Equal(t, "AUR is a dead end for us", line.body)
	assert.Equal(t, "David Heinemeier Hansson", line.who)
}

// An upload is a message with no body: the file is what was said.
func TestAnUploadNamesTheFile(t *testing.T) {
	line := toChatLine(basecamp.CampfireLine{
		ID:   2,
		Type: "Chat::Lines::Upload",
		Attachments: []basecamp.CampfireLineAttachment{
			{Filename: "screenrecording.mp4", ByteSize: 2_600_000},
		},
		Creator: &basecamp.Person{Name: "Stanko K."},
	})

	assert.Equal(t, "📎 screenrecording.mp4 (2.6mb)", line.body)
}

// A line that is only an embed — a tweet, a video — converts to no text at all.
// Its title is the address it embedded, which beats an empty message.
func TestAnEmbedOnlyLineFallsBackToItsTitle(t *testing.T) {
	line := toChatLine(basecamp.CampfireLine{
		ID:      3,
		Content: `<p dir="auto"><figure><iframe width="550" src="https://x.com/i/1"></iframe></figure></p>`,
		Title:   "https://x.com/josh_m_may/status/2094125",
		Creator: &basecamp.Person{Name: "Chase C."},
	})

	assert.Equal(t, "https://x.com/josh_m_may/status/2094125", line.body)
}

// --- How a body reads ---

// A body is Markdown by the time it is drawn, so it is rendered rather than
// printed: the markup turns into styling and stops being characters on screen.
func TestAChatBodyIsRenderedAsMarkdown(t *testing.T) {
	rows := renderBody("**shipped** it, see `bin/ci`", 60)
	require.NotEmpty(t, rows)

	drawn := strings.Join(rows, "\n")
	assert.Contains(t, drawn, "\x1b[", "the body carries no styling")
	assert.Contains(t, ansi.Strip(drawn), "shipped it, see")
	assert.NotContains(t, ansi.Strip(drawn), "**", "the markup is still showing")
}

// A quote and a list get the shapes they have on the web.
func TestAChatBodyKeepsItsShape(t *testing.T) {
	quoted := ansi.Strip(strings.Join(renderBody("> WOW. Just WOW.", 60), "\n"))
	assert.Contains(t, quoted, "│ WOW. Just WOW.")

	listed := ansi.Strip(strings.Join(renderBody("- one\n- two", 60), "\n"))
	assert.Contains(t, listed, "• one")
	assert.Contains(t, listed, "• two")
}

// A line break the writer put in is a line break. HTMLToMarkdown turns a <br>
// into a bare newline, which CommonMark reads as a space.
func TestAChatKeepsTheLineBreaksThatWereWritten(t *testing.T) {
	line := toChatLine(basecamp.CampfireLine{
		ID:      1,
		Content: `<p dir="auto">yay -S hey-cli<br>hey auth login</p>`,
		Creator: &basecamp.Person{Name: "David Heinemeier Hansson"},
	})

	rows := renderBody(line.body, 60)
	require.Len(t, rows, 2, "the two lines were run together: %q", rows)
	assert.Contains(t, ansi.Strip(rows[0]), "yay -S hey-cli")
	assert.Contains(t, ansi.Strip(rows[1]), "hey auth login")
}

// --- The transcript ---

// It reads like a chat: oldest at the top, newest against the composer.
func TestChatReadsOldestFirst(t *testing.T) {
	_, c := openChat(t, 96, 30)
	rendered := ansi.Strip(c.View())

	first := strings.Index(rendered, "AUR is a dead end")
	last := strings.Index(rendered, "Just a small preview")
	require.Positive(t, first)
	assert.Less(t, first, last, "the newest line was drawn above an older one")
}

// The transcript is broken into days, the same way the activity feed is.
func TestChatBreaksIntoDays(t *testing.T) {
	_, c := openChat(t, 96, 30)
	rendered := ansi.Strip(c.View())

	assert.Contains(t, rendered, "TODAY, MONDAY, AUGUST 31")
	assert.Contains(t, rendered, "YESTERDAY, SUNDAY, AUGUST 30")
}

// Somebody still talking is not introduced again.
func TestAChatGroupsWhatOnePersonSaid(t *testing.T) {
	_, c := openChat(t, 96, 30)
	rendered := ansi.Strip(c.View())

	assert.Equal(t, 1, strings.Count(rendered, "Stanko Krtalic Rusendic"),
		"two lines in the same minute said the name twice")
	assert.Contains(t, rendered, "📎 screenrecording.mp4")
}

// A conversation nobody has started says so, rather than showing an empty box.
func TestAQuietChat(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 24)
	c := newChat(m.ctx, testChatTool())
	m.push(c)

	c.Update(chatPageMsg{page: 1})
	m.relayout()

	assert.True(t, c.done)
	assert.Contains(t, ansi.Strip(c.View()), "Nothing said here yet.")
}

// Reaching the top says which end it is, so a conversation that starts there is
// never mistaken for one that failed to load.
func TestTheTopOfAChatSaysSo(t *testing.T) {
	_, c := openChat(t, 96, 30)

	assert.Contains(t, ansi.Strip(c.View()), "The beginning of the chat.")
}

// --- Paging back ---

// Older lines land above the window, so the reader keeps looking at what they
// were looking at.
func TestOlderLinesLandAboveTheWindow(t *testing.T) {
	m, c := openChat(t, 96, 12)
	c.done = false

	before := ansi.Strip(c.View())
	c.Update(chatPageMsg{page: 2, lines: []chatLine{
		said("Jason Fried", 40*time.Hour, "We should be sure to get HEY and Basecamp listed."),
	}})
	m.relayout()

	assert.Equal(t, before, ansi.Strip(c.View()), "a page of older lines moved the window")
	assert.Equal(t, 2, c.page)
}

// A page that failed to arrive leaves what is on screen alone, and stalls the
// walk rather than ending it.
func TestAFailedChatPageKeepsWhatItHas(t *testing.T) {
	m, c := openChat(t, 96, 30)
	c.done = false

	c.Update(chatPageMsg{page: 2, err: errors.New("no route to host")})
	m.relayout()

	assert.True(t, c.stalled)
	assert.False(t, c.done, "one dropped request ended the transcript for good")
	assert.Empty(t, c.notice, "a failed page put a notice over lines that were fine")
	rendered := ansi.Strip(c.View())
	assert.Contains(t, rendered, "AUR is a dead end")
	assert.Contains(t, rendered, "Could not load more")
	assert.Nil(t, m.err)
}

// A first page that fails has nothing to keep, so it says why.
func TestChatFirstPageFailure(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 24)
	c := newChat(m.ctx, testChatTool())
	m.push(c)

	c.Update(chatPageMsg{page: 1, err: errors.New("no route to host")})
	m.relayout()

	assert.Contains(t, c.notice, "Could not load the chat")
	assert.Nil(t, m.err, "a chat read put an error box over the screen")
}

// A page is fifteen lines, which on a tall terminal is half a screen. Each one
// that lands short asks for the one above it, rather than leaving the reader to
// press ↑ at a gap.
func TestAChatKeepsReadingUntilTheScreenIsFull(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 40)
	c := newChat(m.ctx, testChatTool())
	c.now = func() time.Time { return testNow }
	m.push(c)

	cmd, claimed := c.Update(chatPageMsg{page: 1, lines: reversed(testTranscript())})
	require.True(t, claimed)
	require.NotNil(t, cmd, "a short first page did not ask for the one above it")
	assert.True(t, c.paging)

	// Enough to fill the screen, and it stops asking.
	c.paging = false
	full := make([]chatLine, 0, 40)
	for i := range 40 {
		full = append(full, said("Rob Zolkos", time.Duration(i+2)*time.Hour, "another line"))
	}
	cmd, _ = c.Update(chatPageMsg{page: 2, lines: reversed(full)})
	assert.Nil(t, cmd, "a full screen kept reading")
}

// The end of the conversation stops the walk, however short it is.
func TestAChatShorterThanTheScreenStopsAsking(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 40)
	c := newChat(m.ctx, testChatTool())
	m.push(c)

	c.Update(chatPageMsg{page: 1, lines: reversed(testTranscript())})
	c.paging = false

	cmd, _ := c.Update(chatPageMsg{page: 2})
	assert.True(t, c.done)
	assert.Nil(t, cmd, "the beginning of the chat kept reading")
}

// --- Writing ---

// The composer opens on its own key and on enter — there is nothing else to open
// here — and while it is open every key belongs to the message.
func TestTheChatComposerOpens(t *testing.T) {
	m, c := openChat(t, 96, 30)
	assert.Contains(t, ansi.Strip(c.View()), "Write a message")

	m, _ = press(t, m, composeKey)
	assert.True(t, c.writing)
	assert.True(t, c.CapturingInput())

	c.stopWriting()
	_, _ = press(t, m, "enter")
	assert.True(t, c.writing)
}

// Typing goes into the message, not into the shortcuts: the digits are text.
func TestTypingInTheChatComposer(t *testing.T) {
	m, c := openChat(t, 96, 30)
	m, _ = press(t, m, composeKey)

	for _, key := range strings.Split("ok 2", "") {
		m, _ = press(t, m, key)
	}

	assert.Equal(t, "ok 2", c.compose.Value())
	assert.Equal(t, []string{"Home", "Chat"}, m.nav.trail(), "a digit jumped to a section")
}

// Enter sends, and the composer stays open: the next thing to do after saying
// something is usually saying something else.
func TestSendingAMessage(t *testing.T) {
	m, c := openChat(t, 96, 30)
	m, _ = press(t, m, composeKey)
	for _, key := range strings.Split("hi", "") {
		m, _ = press(t, m, key)
	}

	_, cmd := press(t, m, "enter")
	require.NotNil(t, cmd, "enter on a written message sent nothing")
	assert.True(t, c.sending)
	assert.Empty(t, c.compose.Value(), "the sent text was left in the composer")
	assert.True(t, c.writing, "sending closed the composer")
}

// An empty composer sends nothing.
func TestSendingNothing(t *testing.T) {
	m, c := openChat(t, 96, 30)
	m, _ = press(t, m, composeKey)

	_, cmd := press(t, m, "enter")
	assert.Nil(t, cmd)
	assert.False(t, c.sending)
}

// A message that wants a second line asks for one with alt, since enter is what
// sends.
func TestAMessageOnMoreThanOneLine(t *testing.T) {
	m, c := openChat(t, 96, 30)
	m, _ = press(t, m, composeKey)
	for _, key := range strings.Split("one", "") {
		m, _ = press(t, m, key)
	}

	m, cmd := press(t, m, "alt+enter")
	assert.Nil(t, cmd, "alt+enter sent the message")
	for _, key := range strings.Split("two", "") {
		m, _ = press(t, m, key)
	}

	assert.Equal(t, "one\ntwo", c.compose.Value())
	assert.True(t, c.writing)
}

// The composer grows with what is being typed, and the transcript gives up the
// rows it takes.
func TestTheComposerGrowsWithTheMessage(t *testing.T) {
	m, c := openChat(t, 96, 30)
	oneLine := c.transcriptHeight()

	m, _ = press(t, m, composeKey)
	for range 3 {
		m, _ = press(t, m, "alt+enter")
	}
	m.relayout()

	assert.Less(t, c.transcriptHeight(), oneLine, "the transcript kept the composer's rows")
	assert.Equal(t, c.height, len(strings.Split(c.View(), "\n")), "the screen is the wrong height")
}

// What is being typed reads the way it will arrive: the Markdown is styled as it
// is written, and its delimiters dim rather than shouting.
func TestTheComposerStylesTheMarkdownBeingWritten(t *testing.T) {
	m, c := openChat(t, 96, 30)
	m, _ = press(t, m, composeKey)
	for _, key := range strings.Split("**bold**", "") {
		m, _ = press(t, m, key)
	}
	m.relayout()

	rows := c.composerRows()
	require.Len(t, rows, 2)
	assert.Contains(t, rows[1], "\x1b[", "the composer drew no styling")
	// The characters all stay: this is styling laid over the field, not a rewrite.
	assert.Contains(t, ansi.Strip(rows[1]), "**bold**")
}

// The line comes back from the server with its id and time, and goes at the
// newest end — with the window following it down, because the reader wrote it.
func TestASentMessageJoinsTheTranscript(t *testing.T) {
	m, c := openChat(t, 96, 12)
	c.fromBottom = 3

	c.Update(chatSaidMsg{line: said("Stanko K.", 0, "Shipping it.")})
	m.relayout()

	assert.Equal(t, 0, c.fromBottom)
	assert.Contains(t, ansi.Strip(c.View()), "Shipping it.")
}

// A send that failed says so and leaves the text alone, so it can go again
// rather than being written twice.
func TestAFailedSendSaysSo(t *testing.T) {
	m, c := openChat(t, 96, 30)
	c.sending = true

	cmd, claimed := c.Update(chatSaidMsg{err: errors.New("no route to host")})
	require.True(t, claimed)
	require.NotNil(t, cmd)

	assert.False(t, c.sending)
	assert.Contains(t, cmd().(notifyMsg).text, "Could not send the message")
	assert.Nil(t, m.err)
}

// Esc closes the composer and keeps what was typed: it is a way out, not a way
// to lose a message.
func TestEscLeavesTheChatComposer(t *testing.T) {
	m, c := openChat(t, 96, 30)
	m, _ = press(t, m, composeKey)
	for _, key := range strings.Split("wait", "") {
		m, _ = press(t, m, key)
	}

	m, _ = press(t, m, "esc")
	assert.False(t, c.writing)
	assert.Equal(t, "wait", c.compose.Value())
	assert.Equal(t, []string{"Home", "Chat"}, m.nav.trail(), "esc popped the screen instead")

	// A second esc has nothing of its own to close, so it goes back.
	m, _ = press(t, m, "esc")
	assert.Equal(t, []string{"Home"}, m.nav.trail())
}

// --- Where it hangs ---

// The chat is the screen behind the dock's chat tool, and it hangs off the
// project the way any other tool does.
func TestTheChatToolOpensTheChat(t *testing.T) {
	m, _ := openProjectScreen(t, 110)
	for range 3 {
		m, _ = press(t, m, "down")
	}

	m, cmd := press(t, m, "enter")
	m = deliver(t, m, cmd)

	assert.Equal(t, []string{"Home", "CLIs", "Chat"}, m.nav.trail())
	_, ok := m.nav.current().(*chatScreen)
	assert.True(t, ok, "the chat tool opened a placeholder")
}

// --- Layout ---

func TestChatRowsFitTheColumn(t *testing.T) {
	for _, width := range []int{40, 60, 96, 140} {
		_, c := openChat(t, width, 26)
		for _, line := range strings.Split(ansi.Strip(c.View()), "\n") {
			assert.LessOrEqual(t, tui.DisplayWidth(line), c.width, "at terminal width %d", width)
		}
	}
}
