package tui

import (
	"os"
	"strings"
	"sync/atomic"
)

// An image in a terminal is two things sent separately: the pixels, which go
// straight to the terminal as an escape sequence, and the cells that stand where
// the image should appear, which go in the frame like any other text. Kitty calls
// the second part a Unicode placeholder, and it is what lets an image scroll and
// be redrawn with the rest of the screen instead of floating over it.
//
// Ported from hey-cli's internal/tui/image_renderer.go and kitty.go.
type ImageProtocol string

const (
	ImageProtocolText  ImageProtocol = "text"
	ImageProtocolKitty ImageProtocol = "kitty"
)

// ImageProtocolVar settles the question rather than leaving it to be answered:
// BASECAMP_IMAGE_PROTOCOL=kitty draws pictures, =text never does. It is there for
// the terminal that can draw but will not say so, and for the reader who would
// rather have the filename.
const ImageProtocolVar = "BASECAMP_IMAGE_PROTOCOL"

// RenderedImage is one image ready to be shown: Content goes in the view, Raw
// goes to the terminal ahead of it.
//
// Cols and Rows are the cells Content covers. They are the size the placement was
// made for, so a caller with less room than that has to leave the image out rather
// than cut it down: the cells are what the terminal matches the image against, and
// half of them is half an image.
type RenderedImage struct {
	Content string
	Raw     string
	Cols    int
	Rows    int
}

// ImageRenderer draws an image the way the terminal it found can draw one.
type ImageRenderer interface {
	Protocol() ImageProtocol

	// Render answers the cells to draw and the sequence to send. An id must be
	// unique to the image: it is what the placeholder points at.
	Render(data []byte, id, maxCols int) RenderedImage
}

// drawsImages is what the terminal answered when it was asked. Nothing has been
// asked until DetectImageSupport runs, and until then nothing is drawn: a picture
// nobody can see is a screenful of accent marks where the picture should be.
var drawsImages atomic.Bool

// NewImageRenderer is how a picture gets drawn here: the way the terminal said it
// could, or not at all.
//
// Nothing is guessed from the environment. TERM_PROGRAM and a terminal's own
// variables outlive the terminal that set them — they are still there inside tmux,
// inside herdr, inside anything that runs a program in a pane — so what they say
// about what is on the far end of the pty is not worth reading. What the terminal
// answers is.
func NewImageRenderer() ImageRenderer {
	switch strings.ToLower(os.Getenv(ImageProtocolVar)) {
	case string(ImageProtocolKitty):
		return kittyImageRenderer{}
	case string(ImageProtocolText), "none", "off":
		return textImageRenderer{}
	}
	if drawsImages.Load() {
		return kittyImageRenderer{}
	}
	return textImageRenderer{}
}

type textImageRenderer struct{}

type kittyImageRenderer struct{}

func (textImageRenderer) Protocol() ImageProtocol { return ImageProtocolText }

// A terminal that cannot draw an image is handed nothing, and told nothing. What
// stands in its place is the caller's business — in a chat that is the filename and
// its size, which is a whole message on its own. A reader is not made to hear about
// what their terminal cannot do.
func (textImageRenderer) Render([]byte, int, int) RenderedImage { return RenderedImage{} }

func (kittyImageRenderer) Protocol() ImageProtocol { return ImageProtocolKitty }

func (kittyImageRenderer) Render(data []byte, id, maxCols int) RenderedImage {
	cols, rows := imageDimensions(data, maxCols)
	content := renderImagePlaceholder(id, cols, rows)
	if content == "" {
		return RenderedImage{}
	}
	return RenderedImage{
		Content: content,
		Raw:     kittyUploadAndPlace(data, id, cols, rows),
		Cols:    min(cols, len(diacritics)),
		Rows:    min(rows, len(diacritics)),
	}
}
