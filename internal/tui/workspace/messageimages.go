package workspace

import (
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

// How long pictures that have arrived wait before they go on screen together.
//
// They are read one at a time and a message can carry twenty of them, so drawing
// each one as it lands would redraw the screen twenty times — and every redraw
// moves the text under a reader who is already reading it. A second's wait
// collects whatever arrived in it into one redraw.
const imageDrawDelay = time.Second

// reBodyImage matches one image in a message's body. richtext.HTMLToMarkdown
// writes every bc-attachment that is a picture as exactly this, so the body can
// be split on them without parsing the Markdown twice.
var reBodyImage = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)

// bodyPart is a run of a message's body: either prose to render as Markdown, or
// one picture to draw.
//
// A body is split rather than searched. The alternative is rendering the whole
// thing and then hunting for the row an image landed on, which means matching
// against text that already carries escape sequences — and the row moves the
// moment a wrap changes.
type bodyPart struct {
	text string

	// alt and url are set on an image part, and text is empty.
	alt string
	url string
}

func (p bodyPart) isImage() bool { return p.url != "" }

// markdown is the image as it appeared in the body, for a terminal that has no
// picture to show in its place.
func (p bodyPart) markdown() string { return "![" + p.alt + "](" + p.url + ")" }

// splitBody breaks a body into its prose and its pictures, in the order they
// appear.
//
// Splitting costs one thing: an image inside a paragraph ends that paragraph and
// starts a new one after it. Basecamp's editor puts attachments between
// paragraphs, so in practice there is nothing to lose — and a picture drawn as a
// block of cells could not sit mid-sentence anyway.
func splitBody(body string) []bodyPart {
	found := reBodyImage.FindAllStringSubmatchIndex(body, -1)
	if len(found) == 0 {
		if strings.TrimSpace(body) == "" {
			return nil
		}
		return []bodyPart{{text: body}}
	}

	parts := make([]bodyPart, 0, len(found)*2+1)
	at := 0
	for _, where := range found {
		if prose := body[at:where[0]]; strings.TrimSpace(prose) != "" {
			parts = append(parts, bodyPart{text: prose})
		}
		parts = append(parts, bodyPart{
			alt: body[where[2]:where[3]],
			url: body[where[4]:where[5]],
		})
		at = where[1]
	}
	if prose := body[at:]; strings.TrimSpace(prose) != "" {
		parts = append(parts, bodyPart{text: prose})
	}
	return parts
}

// messageImageMsg is one picture that has been read, or an empty one for a read
// that failed. Either way it is the screen's turn to ask for the next.
type messageImageMsg struct {
	source string
	data   []byte
}

// imagesDueMsg is the second's wait ending: whatever arrived in it goes on screen.
type imagesDueMsg struct{}

// imageSpinMsg advances the throbber beside a picture that is still being read.
// Its own message rather than the workspace's spinnerTickMsg: that one is armed
// by the model for the screen-wide spinner, and this screen has content to show
// while it waits.
type imageSpinMsg struct{}

// spinImages arms the throbber, and only while something is actually being read
// so a message whose pictures have all landed wakes up for nothing.
func (m *messageScreen) spinImages() tea.Cmd {
	if m.spinning || !m.readingImages() {
		return nil
	}
	m.spinning = true
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return imageSpinMsg{} })
}

// advanceImageSpinner moves the throbber on a frame and keeps it turning while
// there is still a picture to wait for.
func (m *messageScreen) advanceImageSpinner() tea.Cmd {
	m.spinning = false
	m.spin++
	return m.spinImages()
}

// readingImages reports whether any picture is still on its way — queued, in
// flight, or read and waiting for the next redraw.
// A face still being read counts too: both drive the same throbber, so both
// keep it turning.
func (m *messageScreen) readingImages() bool {
	return len(m.queue) > 0 || len(m.arrived) > 0 || len(m.facesComing) > 0
}

// messageImagesPlacedMsg carries pictures whose pixels the terminal already
// holds, so the cells pointing at them can go on screen.
type messageImagesPlacedMsg struct {
	drawn map[string]tui.RenderedImage
}

// readImages starts the walk through the pictures in the body. A terminal that
// cannot draw them is never asked to: the body already names each one and links
// it, which is all a picture can be there.
func (m *messageScreen) readImages() tea.Cmd {
	if m.images == nil || m.images.Protocol() == tui.ImageProtocolText {
		return nil
	}

	m.queue = nil
	for _, part := range m.parts {
		if source := m.source(part); source != "" && m.pictures[source].Content == "" {
			m.queue = append(m.queue, source)
		}
	}
	return tea.Batch(m.readNextImage(), m.spinImages())
}

// readNextImage takes the next picture off the queue. The queue is what the
// indicator reads too, so a picture is "loading" exactly while it is queued or
// in flight.
func (m *messageScreen) readNextImage() tea.Cmd {
	if len(m.queue) == 0 || m.budget.spent() {
		m.queue = nil
		return nil
	}
	next := m.queue[0]
	return loadMessageImage(m.ctx.Ctx(), m.ctx.app, m.budget, next)
}

// imageArrived takes one picture off the queue and asks for the next, arming the
// wait that will draw whatever has collected by then.
func (m *messageScreen) imageArrived(msg messageImageMsg) tea.Cmd {
	m.queue = without(m.queue, msg.source)
	if len(msg.data) > 0 {
		if m.arrived == nil {
			m.arrived = map[string][]byte{}
		}
		m.arrived[msg.source] = msg.data
	}

	cmds := []tea.Cmd{m.readNextImage(), m.spinImages()}
	if len(m.arrived) > 0 && !m.waiting {
		m.waiting = true
		cmds = append(cmds, tea.Tick(imageDrawDelay, func(time.Time) tea.Msg { return imagesDueMsg{} }))
	}
	return tea.Batch(cmds...)
}

// drawImages sends the terminal the pixels for everything that arrived during the
// wait and only then asks for the cells that point at them, for the reason
// chatScreen.drawImages spells out: Bubble Tea paints the frame before it runs
// the commands, so cells put on screen here would be drawn one frame before the
// pictures they reference exist.
func (m *messageScreen) drawImages() tea.Cmd {
	m.waiting = false
	arrived := m.arrived
	m.arrived = nil

	drawn := make(map[string]tui.RenderedImage, len(arrived))
	var pixels strings.Builder

	for _, part := range m.parts {
		source := m.source(part)
		data, ok := arrived[source]
		if !ok || len(data) == 0 {
			continue
		}
		if _, already := drawn[source]; already {
			continue
		}
		rendered := m.images.Render(data, tui.NextImageID(), m.width)
		if rendered.Content == "" {
			continue
		}
		drawn[source] = rendered
		pixels.WriteString(rendered.Raw)
	}

	if len(drawn) == 0 {
		return nil
	}
	return tea.Sequence(tea.Raw(pixels.String()), placeMessageImages(drawn))
}

func placeMessageImages(drawn map[string]tui.RenderedImage) tea.Cmd {
	return func() tea.Msg { return messageImagesPlacedMsg{drawn: drawn} }
}

func (m *messageScreen) placeImages(drawn map[string]tui.RenderedImage) {
	if m.pictures == nil {
		m.pictures = map[string]tui.RenderedImage{}
	}
	for source, rendered := range drawn {
		m.pictures[source] = rendered
	}
}

// source is where a picture in the body is read from, and empty for a part that
// is not a picture or is one with no attachment behind it. See imageSources.
func (m *messageScreen) source(part bodyPart) string {
	if !part.isImage() {
		return ""
	}
	return m.post.images[part.url]
}

// coming reports whether a picture is still on its way, which is what the row
// standing in for it says while it waits.
func (m *messageScreen) coming(part bodyPart) bool {
	source := m.source(part)
	if source == "" {
		return false
	}
	if _, waiting := m.arrived[source]; waiting {
		return true
	}
	for _, queued := range m.queue {
		if queued == source {
			return true
		}
	}
	return false
}

// comingLabel names the picture a reader is waiting on, behind a throbber, by
// its caption when it has one.
func (m *messageScreen) comingLabel(part bodyPart) string {
	frame := spinnerFrames[m.spin%len(spinnerFrames)]
	if part.alt == "" {
		return frame + " Loading image…"
	}
	return frame + " Loading " + part.alt + "…"
}

// picture is the cells for one image, and nothing when there are none to draw or
// the column has since become too narrow for the size they were drawn at.
func (m *messageScreen) picture(part bodyPart) []string {
	rendered := m.pictures[m.source(part)]
	if rendered.Content == "" || rendered.Cols > m.width {
		return nil
	}
	return strings.Split(rendered.Content, "\n")
}

func without(sources []string, source string) []string {
	kept := make([]string, 0, len(sources))
	for _, each := range sources {
		if each != source {
			kept = append(kept, each)
		}
	}
	return kept
}
