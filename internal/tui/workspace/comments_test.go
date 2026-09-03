package workspace

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// withComments hands a list two comments, the way a read would.
func withComments(t *testing.T, ctx *Context) *commentList {
	t.Helper()

	answers := newCommentList(ctx)
	answers.recording = 5
	answers.update(commentsMsg{recording: 5, comments: []comment{
		{id: 1, author: person{name: "Rob Z."}, words: wrote("Sounds good."), at: testNow.Add(-2 * time.Minute)},
		{id: 2, author: person{name: "Jorge L."}, words: wrote("On it."), at: testNow.Add(-time.Minute)},
	}})
	return answers
}

// The cursor starts on the recording rather than on its first answer, and j and
// k walk down into the comments and back off the top of them.
func TestTheCommentCursorStartsAboveTheComments(t *testing.T) {
	answers := withComments(t, &Context{styles: tui.NewStylesWithTheme(tui.DefaultTheme(true))})

	if _, standing := answers.selected(); standing {
		t.Error("a comment was selected before the reader asked for one")
	}

	answers.handleKey(keyPress(nextCommentKey))
	selected, standing := answers.selected()
	if !standing || selected.id != 1 {
		t.Errorf("j selected %v, want the first comment", selected.id)
	}

	answers.handleKey(keyPress(prevCommentKey))
	if _, standing := answers.selected(); standing {
		t.Error("k off the top of the comments should come back to the recording")
	}
}

// enter offers what can be done to the comment under the cursor, and nothing
// while the reader is on the recording itself.
func TestEnterOffersTheCommentsActions(t *testing.T) {
	answers := withComments(t, &Context{styles: tui.NewStylesWithTheme(tui.DefaultTheme(true))})

	if cmd, _ := answers.handleKey(keyPress("enter")); cmd != nil {
		t.Errorf("enter on the recording offered %T", cmd())
	}

	answers.handleKey(keyPress(nextCommentKey))
	cmd, took := answers.handleKey(keyPress("enter"))
	if !took || cmd == nil {
		t.Fatal("enter on a comment offered nothing")
	}
	if _, ok := cmd().(commentActionsMsg); !ok {
		t.Errorf("enter answered %T, want commentActionsMsg", cmd())
	}
}

// A card's comments answer the same keys a message's do — that is the point of
// them being one component.
func TestACardsCommentsAnswerTheSameKeys(t *testing.T) {
	m := resize(t, newTestModel(t), 110, 40)
	screen := newCardScreen(m.ctx, card{id: 5, title: "Improve the flow", comments: 2})
	screen.now = func() time.Time { return testNow }
	m.push(screen)

	screen.answers.update(commentsMsg{recording: 5, comments: []comment{
		{id: 1, author: person{name: "Rob Z."}, words: wrote("Sounds good."), at: testNow.Add(-2 * time.Minute)},
	}})
	m.relayout()

	if got := ansi.Strip(screen.View()); !strings.Contains(got, "Sounds good.") {
		t.Fatalf("the comment was not drawn:\n%s", got)
	}

	screen.HandleKey(keyPress(nextCommentKey))
	if _, standing := screen.answers.selected(); !standing {
		t.Fatal("j did not walk into the comments")
	}
	if got := ansi.Strip(screen.View()); !strings.Contains(got, selectedMarker) {
		t.Errorf("the selected comment carries no marker:\n%s", got)
	}

	opened := screen.HandleKey(keyPress("enter"))
	if opened == nil {
		t.Fatal("enter on a card's comment offered nothing")
	}
	if _, ok := opened().(commentActionsMsg); !ok {
		t.Errorf("enter answered %T, want commentActionsMsg", opened())
	}
}

// The reactions on a comment sit inside its block wherever it is read, so they
// line up under the words rather than under the face.
func TestTheCommentsBoostsStayInsideTheBlock(t *testing.T) {
	styles := tui.NewStylesWithTheme(tui.DefaultTheme(true))
	answers := withComments(t, &Context{styles: styles})
	answers.boosts[1] = []boost{{id: 9, content: "👍", by: person{name: "Andy Smith"}}}

	shown := newPictures(&Context{styles: styles})
	drawn := strings.Join(answers.rows(styles, shown, 80, testNow, false), "\n")

	if !strings.Contains(ansi.Strip(drawn), "╭") {
		t.Errorf("the reactions were never boxed:\n%s", ansi.Strip(drawn))
	}
}
