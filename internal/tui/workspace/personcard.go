package workspace

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// The key that asks who somebody is — the author of whatever the reader is
// standing on.
const personCardKey = "i"

// personCardMsg asks the model to open a card about somebody.
type personCardMsg struct{ who person }

// personCard is who somebody is: their picture, their name, what they do, where
// they are and what time it is there.
//
// The web's own card offers a column of things to do next — what they have been
// up to, their tasks, their files. None of those are reachable from here yet, so
// this is the half that is: who am I reading, and is it the middle of their
// night.
type personCard struct {
	ctx *Context
	who person

	// face is the picture, once the terminal holds it. Read here rather than
	// borrowed from the screen underneath: a card is opened over any screen, and
	// only this one knows it wants a picture this size.
	face   tui.RenderedImage
	budget *imageBudget
	images tui.ImageRenderer

	// coming says the picture was asked for and has not landed, which the
	// throbber in its place stands for.
	coming   bool
	spin     int
	spinning bool

	wide int
	now  func() time.Time
}

// cardFaceCols is how wide the picture on a card is drawn. Bigger than the four
// cells a comment gives it — this is a card about one person, and the picture is
// the first thing on it.
const cardFaceCols = 16

func newPersonCard(ctx *Context, who person) *personCard {
	return &personCard{
		ctx:    ctx,
		who:    who,
		budget: newFaceBudget(),
		images: tui.NewImageRenderer(),
		now:    time.Now,
	}
}

func (c *personCard) Init() tea.Cmd {
	if c.who.avatar == "" || c.images.Protocol() == tui.ImageProtocolText {
		return nil
	}
	c.coming = true
	return tea.Batch(
		loadCardFace(c.ctx.Ctx(), c.ctx.app, c.budget, c.who.avatar),
		c.spin4(),
	)
}

func (c *personCard) Title() string { return c.who.name }

// HandleKey closes on anything that means "done". There is nothing to choose
// here: a card is something to read.
func (c *personCard) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch msg.Key().Code {
	case tea.KeyEsc, tea.KeyEnter:
		return nil, false
	}
	if msg.String() == "q" || msg.String() == "i" {
		return nil, false
	}
	return nil, true
}

func (c *personCard) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case cardFaceMsg:
		if len(msg.data) == 0 {
			c.coming = false
			return nil, true
		}
		rendered := c.images.Render(msg.data, tui.NextImageID(), cardFaceCols)
		if rendered.Content == "" {
			c.coming = false
			return nil, true
		}
		// The pixels first and the cells behind them, the same order and for the
		// same reason as everywhere else a picture is drawn.
		return tea.Sequence(tea.Raw(rendered.Raw), func() tea.Msg {
			return cardFacePlacedMsg{rendered: rendered}
		}), true

	case cardFacePlacedMsg:
		c.face, c.coming = msg.rendered, false
		return nil, true

	case cardSpinMsg:
		c.spinning = false
		c.spin++
		return c.spin4(), true
	}
	return nil, false
}

// cardFacePlacedMsg carries a picture whose pixels the terminal already holds.
type cardFacePlacedMsg struct{ rendered tui.RenderedImage }

// cardSpinMsg advances the throbber standing in for the picture.
type cardSpinMsg struct{}

func (c *personCard) spin4() tea.Cmd {
	if c.spinning || !c.coming {
		return nil
	}
	c.spinning = true
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return cardSpinMsg{} })
}

// --- Rendering ---

func (c *personCard) View() string {
	styles := c.ctx.Styles()
	does := lipgloss.NewStyle().Foreground(styles.Theme().Primary)

	var rows []string
	if picture := c.picture(); len(picture) > 0 {
		rows = append(rows, picture...)
		rows = append(rows, "")
	}

	// The name is the frame's title, so it is not repeated here.
	if where := c.who.where(); where != "" {
		rows = append(rows, wrapText(does.Render(where), c.width())...)
	}

	// Where they are and what time it is there, on one line: the two halves of
	// "is it the middle of their night".
	if here := strings.Join(nonEmpty(c.who.location, c.who.clock(c.now())), " · "); here != "" {
		rows = append(rows, styles.Muted.Render(truncateToWidth(here, c.width())))
	}

	if c.who.bio != "" {
		rows = append(rows, "", strings.Repeat("─", min(c.width(), 24)), "")
		rows = append(rows, wrapText(c.who.bio, c.width())...)
	}
	if c.who.email != "" {
		rows = append(rows, "", styles.Muted.Render(truncateToWidth(c.who.email, c.width())))
	}
	return strings.Join(rows, "\n")
}

// picture is the cells for the face, a throbber while it is coming, and nothing
// when there is none — a card about somebody with no picture is their name, not
// their name under a gap.
func (c *personCard) picture() []string {
	if c.face.Content != "" && c.face.Cols <= c.width() {
		return strings.Split(c.face.Content, "\n")
	}
	if !c.coming {
		return nil
	}
	return []string{c.ctx.Styles().Muted.Render(
		spinnerFrames[c.spin%len(spinnerFrames)] + " Loading…")}
}

func (c *personCard) HelpBindings() []helpBinding {
	return []helpBinding{{"esc", "close"}}
}

func (c *personCard) Resize(width, height int) { c.wide = width }

func (c *personCard) width() int { return max(c.wide, 1) }

// Widest is how much room the card actually wants: the longest thing on it, or
// the picture, whichever is wider.
//
// A card is a name and three short lines under it. Given three fifths of the
// terminal it draws a name in the corner of a mostly empty box, which reads as a
// dialog waiting for something rather than as a card about somebody.
func (c *personCard) Widest() int {
	widest := cardFaceCols
	for _, line := range []string{c.who.name, c.who.where(), c.who.bio, c.who.email,
		strings.Join(nonEmpty(c.who.location, c.who.clock(c.now())), " · ")} {
		widest = max(widest, tui.DisplayWidth(line))
	}
	return widest
}
