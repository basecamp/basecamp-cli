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

// messageImagesPlacedMsg carries pictures whose pixels the terminal already
// holds, so the cells pointing at them can go on screen.
type messageImagesPlacedMsg struct {
	drawn map[string]tui.RenderedImage
}

func placeMessageImages(drawn map[string]tui.RenderedImage) tea.Cmd {
	return func() tea.Msg { return messageImagesPlacedMsg{drawn: drawn} }
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
