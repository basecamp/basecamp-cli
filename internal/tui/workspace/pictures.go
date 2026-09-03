package workspace

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// pictures is every image a screen draws: the ones inside a body, and the ones
// beside people's names.
//
// It was the message screen's alone, and every screen that shows a body or a
// comment needs the same thing — so it is a component the screens hold rather
// than a dozen fields each of them keeps a copy of.
//
// The two kinds are read on budgets of their own rather than one shared. Two
// reasons, and both bit: the reads are batched, so one budget would be counted
// down from two goroutines at once; and a body carrying twenty screenshots would
// spend every slot on them and leave nothing for the faces, which is exactly
// what it did.
type pictures struct {
	ctx      *Context
	renderer tui.ImageRenderer
	width    int

	// drawn are the pictures the terminal already holds, by the address each was
	// read from. queue is what is still to read, in the order they appear;
	// arrived is what has been read and is waiting for the next redraw; waiting
	// says a wait is already armed, so a run of arrivals arms one rather than one
	// each.
	drawn   map[string]tui.RenderedImage
	budget  *imageBudget
	queue   []string
	arrived map[string][]byte
	waiting bool

	// spin is the throbber's frame, and spinning says one tick is already in
	// flight so a run of arrivals does not arm one each.
	spin     int
	spinning bool

	// faces are people's pictures, one per person and size rather than one per
	// comment: a thread is mostly the same few people. facesComing is what has
	// been asked for and is not on screen yet, which is what the throbber stands
	// for — by person, because one read answers every size of them.
	faces       map[faceAt]tui.RenderedImage
	faceBudget  *imageBudget
	facesComing map[string]struct{}
}

// faceAt is one person's picture at one size. A face is drawn two ways — the
// square beside their name and the sliver on a reaction — and the terminal holds
// a rendering per size rather than stretching one to fit the other.
type faceAt struct {
	source string
	cols   int
}

func newPictures(ctx *Context) *pictures {
	return &pictures{
		ctx:         ctx,
		renderer:    tui.NewImageRenderer(),
		drawn:       map[string]tui.RenderedImage{},
		budget:      newImageBudget(),
		faces:       map[faceAt]tui.RenderedImage{},
		faceBudget:  newFaceBudget(),
		facesComing: map[string]struct{}{},
	}
}

// resize sets the width pictures are rendered at, which is the screen's.
//
// One rendering per picture, at one width. A picture inside a comment is drawn
// in a narrower column than one in a body — indented past the author's face —
// and rows is what checks a rendering against the column it is going into: too
// wide for it and the picture gives way to its own name and a link. Rendering
// the same picture twice at two widths would mean transmitting it twice.
func (p *pictures) resize(width int) { p.width = max(width, 1) }

// drawable is whether the terminal can draw a picture at all. One that cannot is
// never asked to: a body already names each image and links it, which is all a
// picture can be there.
func (p *pictures) drawable() bool {
	return p.renderer != nil && p.renderer.Protocol() != tui.ImageProtocolText
}

// forget drops what has been read, for a body that has been rewritten: what the
// words say has changed, and so may which pictures they carry.
func (p *pictures) forget() {
	p.drawn = map[string]tui.RenderedImage{}
	p.budget = newImageBudget()
}

// busy reports whether any picture is still on its way — queued, in flight, or
// read and waiting for the next redraw. A face counts too: both kinds drive the
// same throbber, so both keep it turning.
func (p *pictures) busy() bool {
	return len(p.queue) > 0 || len(p.arrived) > 0 || len(p.facesComing) > 0
}

// --- Reading ---

// read starts the walk through the pictures in one or more bodies — a screen's
// own, and the ones its comments carry.
//
// Every body a screen holds goes through here rather than each getting its own
// walk. One queue and one budget across all of them is the point: twenty
// screenshots in a message and twenty more in the answers to it should not be
// two runs of twenty.
func (p *pictures) read(bodies ...body) tea.Cmd {
	if !p.drawable() {
		return nil
	}

	p.queue = nil
	for _, shown := range bodies {
		for _, part := range shown.parts {
			source := shown.source(part)
			if source == "" || p.drawn[source].Content != "" || slices.Contains(p.queue, source) {
				continue
			}
			p.queue = append(p.queue, source)
		}
	}
	return tea.Batch(p.readNext(), p.spin1())
}

// readNext takes the next picture off the queue. The queue is what the throbber
// reads too, so a picture is "loading" exactly while it is queued or in flight.
func (p *pictures) readNext() tea.Cmd {
	if len(p.queue) == 0 || p.budget.spent() {
		p.queue = nil
		return nil
	}
	return loadMessageImage(p.ctx.Ctx(), p.ctx.app, p.budget, p.queue[0])
}

// readFaces asks for the pictures of everyone named, skipping the ones already
// drawn and the ones already on their way.
//
// The skip matters: this runs more than once on a screen — once for the author
// when it opens and once for everybody when the comments land — and without it
// the author was asked for again while the first read was still in flight, which
// put two reads over one budget at once.
func (p *pictures) readFaces(people []string) tea.Cmd {
	if !p.drawable() {
		return nil
	}

	wanted := make([]string, 0, len(people))
	for _, source := range people {
		drawn := p.faces[faceAt{source: source, cols: avatarCols}]
		if source == "" || drawn.Content != "" || slices.Contains(wanted, source) {
			continue
		}
		if _, coming := p.facesComing[source]; coming {
			continue
		}
		wanted = append(wanted, source)
	}
	if len(wanted) == 0 {
		return nil
	}

	for _, source := range wanted {
		p.facesComing[source] = struct{}{}
	}
	return tea.Batch(loadAvatars(p.ctx.Ctx(), p.ctx.app, p.faceBudget, wanted), p.spin1())
}

// update takes the four messages the reads answer with, and says whether it took
// one. Every screen holding pictures forwards its messages here.
func (p *pictures) update(msg tea.Msg, bodies ...body) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case messageImageMsg:
		return p.arrivedOne(msg), true

	case imagesDueMsg:
		return p.draw(bodies...), true

	case messageImagesPlacedMsg:
		p.place(msg.drawn)
		return nil, true

	case avatarsMsg:
		p.facesArrived(msg.asked, msg.avatars)
		return p.drawFaces(msg.avatars), true

	case facesPlacedMsg:
		p.placeFaces(msg.drawn)
		return nil, true

	case imageSpinMsg:
		p.spinning = false
		p.spin++
		return p.spin1(), true
	}
	return nil, false
}

// arrivedOne takes one picture off the queue and asks for the next, arming the
// wait that will draw whatever has collected by then.
func (p *pictures) arrivedOne(msg messageImageMsg) tea.Cmd {
	p.queue = without(p.queue, msg.source)
	if len(msg.data) > 0 {
		if p.arrived == nil {
			p.arrived = map[string][]byte{}
		}
		p.arrived[msg.source] = msg.data
	}

	cmds := []tea.Cmd{p.readNext(), p.spin1()}
	if len(p.arrived) > 0 && !p.waiting {
		p.waiting = true
		cmds = append(cmds, tea.Tick(imageDrawDelay, func(time.Time) tea.Msg { return imagesDueMsg{} }))
	}
	return tea.Batch(cmds...)
}

// spin1 arms the throbber, and only while something is actually being read so a
// screen whose pictures have all landed does not wake up for nothing.
func (p *pictures) spin1() tea.Cmd {
	if p.spinning || !p.busy() {
		return nil
	}
	p.spinning = true
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return imageSpinMsg{} })
}

// --- Drawing ---

// draw sends the terminal the pixels for everything that arrived during the wait
// and only then asks for the cells that point at them: Bubble Tea paints the
// frame before it runs the commands, so cells put on screen here would be drawn
// one frame before the pictures they reference exist.
func (p *pictures) draw(bodies ...body) tea.Cmd {
	p.waiting = false
	arrived := p.arrived
	p.arrived = nil

	fresh := make(map[string]tui.RenderedImage, len(arrived))
	var pixels strings.Builder

	for _, shown := range bodies {
		for _, part := range shown.parts {
			source := shown.source(part)
			data, ok := arrived[source]
			if !ok || len(data) == 0 {
				continue
			}
			if _, already := fresh[source]; already {
				continue
			}
			rendered := p.renderer.Render(data, tui.NextImageID(), p.width)
			if rendered.Content == "" {
				continue
			}
			fresh[source] = rendered
			pixels.WriteString(rendered.Raw)
		}
	}

	if len(fresh) == 0 {
		return nil
	}
	return tea.Sequence(tea.Raw(pixels.String()), placeMessageImages(fresh))
}

func (p *pictures) place(fresh map[string]tui.RenderedImage) {
	for source, rendered := range fresh {
		p.drawn[source] = rendered
	}
}

// rows is a body drawn into a column of its own width: its prose as Markdown,
// its pictures as cells, and a throbber where one is still on its way.
//
// This is the one place a body becomes rows. A screen that drew it itself would
// have to decide all over again what to do about a picture the terminal cannot
// show, and two screens deciding that separately is two answers.
func (p *pictures) rows(shown body, styles *tui.Styles, width int) []string {
	var rows []string
	for _, part := range shown.parts {
		if !part.isImage() {
			rows = append(rows, renderBody(part.text, width)...)
			continue
		}
		if cells := p.picture(shown, part, width); cells != nil {
			rows = append(rows, cells...)
			if part.alt != "" {
				rows = append(rows, styles.Muted.Render(truncateToWidth(part.alt, width)))
			}
			rows = append(rows, "")
			continue
		}
		if p.coming(shown, part) {
			// A picture on its way says so where it will appear, so a reader
			// watching a body full of screenshots knows to wait rather than
			// taking the gap for the whole thing.
			rows = append(rows, styles.Muted.Render(truncateToWidth(p.comingLabel(part), width)), "")
			continue
		}
		// No picture to draw — the terminal cannot draw one, the read failed, or
		// the column is narrower than the picture was rendered for. What the
		// image was called and where it lives is what is left to say, which is
		// what renderBody makes of the image markup on its own.
		rows = append(rows, renderBody(part.markdown(), width)...)
	}
	return rows
}

// picture is the cells for one image, and nothing when there are none to draw or
// the column it is going into is narrower than the size they were drawn at.
func (p *pictures) picture(shown body, part bodyPart, width int) []string {
	rendered := p.drawn[shown.source(part)]
	if rendered.Content == "" || rendered.Cols > width {
		return nil
	}
	return strings.Split(rendered.Content, "\n")
}

// coming reports whether a picture is still on its way, which is what the row
// standing in for it says while it waits.
func (p *pictures) coming(shown body, part bodyPart) bool {
	source := shown.source(part)
	if source == "" {
		return false
	}
	if _, waiting := p.arrived[source]; waiting {
		return true
	}
	return slices.Contains(p.queue, source)
}

// comingLabel names the picture a reader is waiting on, behind a throbber, by
// its caption when it has one.
func (p *pictures) comingLabel(part bodyPart) string {
	frame := spinnerFrames[p.spin%len(spinnerFrames)]
	if part.alt == "" {
		return frame + " Loading image…"
	}
	return frame + " Loading " + part.alt + "…"
}

// --- Faces ---

// drawFaces sends the pixels for the pictures that arrived and then asks for
// their cells, the same two steps and the same reason as draw.
func (p *pictures) drawFaces(arrived map[string][]byte) tea.Cmd {
	fresh := p.renderFaces(arrived)
	if len(fresh) == 0 {
		return nil
	}

	// Each face's pixels, then that same face's cells, then the next one.
	//
	// Not every payload and then every placement. A picture's cells name an image
	// the terminal must already hold, so putting them all up after the last write
	// leaves whichever face was written last with its cells on screen a frame
	// early — and since these arrive as a map, which face that was changed every
	// run. Pairing each write with its own placement means no face's cells can
	// out-run its own pixels.
	sends := make([]tea.Cmd, 0, len(fresh)*2)
	for at, rendered := range fresh {
		sends = append(sends, tea.Raw(rendered.Raw), placeFaces(map[faceAt]tui.RenderedImage{at: rendered}))
	}
	return tea.Sequence(sends...)
}

// renderFaces draws each face that arrived at both the sizes a screen shows one
// at.
//
// Two renderings of the same bytes rather than one scaled: the terminal fills
// whatever cells a picture is placed in, so a square meant for four columns put
// into two comes out squeezed. One read, two transmits, and both are a dozen
// kilobytes.
func (p *pictures) renderFaces(arrived map[string][]byte) map[faceAt]tui.RenderedImage {
	fresh := make(map[faceAt]tui.RenderedImage, len(arrived)*2)
	for source, data := range arrived {
		if len(data) == 0 {
			continue
		}
		for _, cols := range []int{avatarCols, chipCols} {
			if rendered := p.renderer.Render(data, tui.NextImageID(), cols); rendered.Content != "" {
				fresh[faceAt{source: source, cols: cols}] = rendered
			}
		}
	}
	return fresh
}

func (p *pictures) placeFaces(fresh map[faceAt]tui.RenderedImage) {
	for at, rendered := range fresh {
		p.faces[at] = rendered
		delete(p.facesComing, at.source)
	}
}

// facesArrived forgets the ones that came back empty, so the throbber stops
// standing in for a picture that is never going to appear.
func (p *pictures) facesArrived(asked []string, arrived map[string][]byte) {
	for _, source := range asked {
		if len(arrived[source]) == 0 {
			delete(p.facesComing, source)
		}
	}
}

// face is what stands in a person's column: the cells once the terminal holds
// their picture, a throbber while it is on its way, and nothing at all for
// somebody who has none — a row without a picture is laid out without the space
// for one rather than with a hole in it.
func (p *pictures) face(source string) []string {
	if rendered := p.faces[faceAt{source: source, cols: avatarCols}]; rendered.Content != "" {
		return strings.Split(rendered.Content, "\n")
	}
	if _, coming := p.facesComing[source]; coming {
		return p.faceComing()
	}
	return nil
}

// chip is a person's face at the size a reaction has room for: two cells wide
// and one row tall, which is about square once a cell's own proportions are
// counted. There is no throbber for one — a reaction is a small thing beside a
// comment, and something blinking on each of them is not.
func (p *pictures) chip(source string) string {
	rendered := p.faces[faceAt{source: source, cols: chipCols}]
	if rendered.Content == "" || strings.Contains(rendered.Content, "\n") {
		return ""
	}
	return rendered.Content
}

// faceComing is the throbber in the middle of the square the picture will fill,
// so the name beside it does not shift left and then right again when it lands.
func (p *pictures) faceComing() []string {
	frame := p.ctx.Styles().Muted.Render(spinnerFrames[p.spin%len(spinnerFrames)])
	blank := strings.Repeat(" ", avatarCols)

	rows := make([]string, 0, avatarRows)
	for row := range avatarRows {
		if row == avatarRows/2 {
			// Centered in the square: one cell of throbber, the rest padding.
			rows = append(rows, strings.Repeat(" ", (avatarCols-1)/2)+frame+
				strings.Repeat(" ", avatarCols-1-(avatarCols-1)/2))
			continue
		}
		rows = append(rows, blank)
	}
	return rows
}
