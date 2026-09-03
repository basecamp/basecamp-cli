package workspace

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// cardScreen is one card: what it is called, who has it, what was written on it,
// the steps it was broken into, and what has been said about it.
//
// The card itself is read once, by the board: a column's index renders the
// description and the steps along with the cards, so the words are already here
// when the screen opens. What it opens is the comments, the pictures in the
// description, and the faces of whoever answered — the same three components a
// message holds, for the same reasons.
type cardScreen struct {
	ctx  *Context
	card card

	words   body
	shown   *pictures
	answers *commentList

	offset int
	width  int
	height int

	now func() time.Time
}

func newCardScreen(ctx *Context, on card) *cardScreen {
	return &cardScreen{
		ctx:     ctx,
		card:    on,
		words:   on.words,
		shown:   newPictures(ctx),
		answers: newCommentList(ctx),
		now:     time.Now,
	}
}

func (c *cardScreen) Init() tea.Cmd {
	return tea.Batch(
		c.shown.read(c.words),
		c.answers.read(c.card.id, c.card.comments),
	)
}

func (c *cardScreen) Title() string { return c.card.title }

func (c *cardScreen) Loading() bool { return false }

func (c *cardScreen) Resize(width, height int) {
	c.width = width
	c.height = height
	c.shown.resize(width)
	c.clampScroll()
}

// Update hands what arrives to whichever component it belongs to, the same way
// the message screen does.
func (c *cardScreen) Update(msg tea.Msg) (tea.Cmd, bool) {
	if cmd, took := c.answers.update(msg); took {
		c.clampScroll()
		return tea.Batch(cmd,
			c.shown.readFaces(c.answers.people()),
			c.shown.read(c.words, c.answers.carried())), true
	}
	if cmd, took := c.shown.update(msg, c.words, c.answers.carried()); took {
		c.clampScroll()
		return cmd, true
	}
	return nil, false
}

// HandleKey scrolls the card and walks what has been said about it. The comments
// answer first, so enter on one means here what it means under a message.
func (c *cardScreen) HandleKey(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, took := c.answers.handleKey(msg); took {
		c.scrollToSelected()
		return cmd
	}
	if msg.String() == personCardKey {
		return c.openCard()
	}

	switch msg.Key().Code {
	case tea.KeyUp:
		c.offset = max(c.offset-1, 0)
	case tea.KeyDown:
		c.offset = min(c.offset+1, c.bottom())
	case tea.KeyPgUp:
		c.offset = max(c.offset-c.height, 0)
	case tea.KeyPgDown:
		c.offset = min(c.offset+c.height, c.bottom())
	case tea.KeyHome:
		c.offset = 0
	case tea.KeyEnd:
		c.offset = c.bottom()
	}
	return nil
}

// openCard says who wrote what the reader is standing on: the selected comment's
// author, or whoever is holding the card when they are reading the card itself.
func (c *cardScreen) openCard() tea.Cmd {
	who := person{name: c.card.author, avatar: c.card.avatar}
	if selected, standing := c.answers.selected(); standing {
		who = selected.author
	}
	if !who.known() {
		return nil
	}
	return func() tea.Msg { return personCardMsg{who: who} }
}

// scrollToSelected brings the selected comment into view.
func (c *cardScreen) scrollToSelected() {
	rows := c.layout()
	c.offset = scrollTo(markedSpan(rows), c.offset, c.height, len(rows))
}

func (c *cardScreen) HelpBindings() []helpBinding {
	return append([]helpBinding{{"↑↓", "scroll"}}, c.answers.helpBindings()...)
}

// bottom is as far down as the card can be scrolled: the last screenful.
func (c *cardScreen) bottom() int { return max(len(c.layout())-c.height, 0) }

// clampScroll keeps the scroll inside what there is to read, for when the
// terminal grows under a screen that was scrolled to the end of it — or when a
// picture lands and the rows below it move.
func (c *cardScreen) clampScroll() {
	c.offset = max(min(c.offset, c.bottom()), 0)
}

// --- Rendering ---

func (c *cardScreen) View() string {
	rows := c.layout()
	end := min(c.offset+c.height, len(rows))
	return strings.Join(rows[min(c.offset, end):end], "\n")
}

func (c *cardScreen) layout() []string {
	styles := c.ctx.Styles()
	heading := lipgloss.NewStyle().Foreground(styles.Theme().Foreground).Bold(true)
	now := c.now()

	// The title leads, with whoever is holding the card beside their picture
	// under it, the way a message leads with its subject.
	face := c.shown.face(c.card.avatar)
	inner := max(c.width, 1)
	if len(face) > 0 {
		inner = max(inner-avatarCols-2, 1)
	}
	header := append(
		wrapText(heading.Render(truncateToWidth(c.card.title, inner)), inner),
		styles.Muted.Render(c.card.byline(now, inner)),
	)

	rows := append(besideFace(face, header), "")
	rows = append(rows, c.shown.rows(c.words, styles, c.width)...)
	rows = append(rows, c.stepRows(styles, heading)...)

	rows = append(rows, ruledHeading(styles, c.card.talk(), heading, c.width, c.shown.busy()))
	return append(rows, c.answers.rows(styles, c.shown, c.width, now, true)...)
}

// stepRows is the checklist a card was broken into, with the heading saying how
// far down it the work has got.
func (c *cardScreen) stepRows(styles *tui.Styles, heading lipgloss.Style) []string {
	if len(c.card.stepList) == 0 {
		return nil
	}

	rows := []string{ruledHeading(styles, "Steps · "+c.card.stepsDoneOf(), heading, c.width, false)}
	for _, step := range c.card.stepList {
		rows = append(rows, c.renderStep(styles, step))
	}
	return append(rows, "")
}

func (c *cardScreen) renderStep(styles *tui.Styles, step cardStep) string {
	box, name := "☐", lipgloss.NewStyle().Foreground(styles.Theme().Foreground)
	if step.done {
		box, name = "☑", styles.Muted
	}
	inner := max(c.width-4, 1)

	line := "  " + styles.Muted.Render(box) + " " + name.Render(truncateToWidth(step.title, inner))
	if step.who == "" {
		return line
	}
	return line + styles.Muted.Render(" — "+step.who)
}

// stepsDoneOf is the steps as a fraction, for the heading over them.
func (c card) stepsDoneOf() string {
	return strconv.Itoa(c.stepsDone) + " of " + strconv.Itoa(c.steps) + " done"
}

// talk is how much has been said about the card, which is what the heading over
// the comments says — and what it says when there is nothing under it.
func (c card) talk() string {
	switch c.comments {
	case 0:
		return "No comments"
	case 1:
		return "1 comment"
	default:
		return strconv.Itoa(c.comments) + " comments"
	}
}
