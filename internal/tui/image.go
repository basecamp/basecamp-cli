package tui

import (
	"os"
	"strings"
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
	ImageProtocolSixel ImageProtocol = "sixel"
)

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

type textImageRenderer struct{}

type kittyImageRenderer struct{}

// NewImageRenderer picks the renderer for the terminal the environment describes.
func NewImageRenderer() ImageRenderer { return SelectImageRenderer(os.Getenv) }

func SelectImageRenderer(lookupEnv func(string) string) ImageRenderer {
	switch DetectImageCapability(lookupEnv) {
	case ImageProtocolKitty:
		return kittyImageRenderer{}
	case ImageProtocolSixel:
		// Sixel graphics are cursor-positioned. Bubble Tea redraws the cell grid
		// after raw output, so a Sixel image lands wherever the cursor happened to
		// be and survives one frame. Until there is a stable placement for it, a
		// Sixel terminal gets the same words a plain one does.
		return textImageRenderer{}
	default:
		return textImageRenderer{}
	}
}

// ImageProtocolVar is the environment variable that settles the question rather
// than leaving it to be guessed: BASECAMP_IMAGE_PROTOCOL=kitty draws pictures,
// =text never does.
const ImageProtocolVar = "BASECAMP_IMAGE_PROTOCOL"

// DetectImageCapability works out what the terminal on the other end can draw.
//
// Getting this wrong in either direction costs something. Guess too low and a
// terminal that can show pictures shows filenames instead. Guess too high and the
// placeholder cells are drawn as what they are made of — a private-use character
// and two combining marks per cell — which is a screen of accent soup where a
// picture should be. So every signal is read, a multiplexer in between is enough to
// say no, and there is a variable to say outright, because no set of signals is
// going to be right about every terminal.
func DetectImageCapability(lookupEnv func(string) string) ImageProtocol {
	switch strings.ToLower(lookupEnv(ImageProtocolVar)) {
	case string(ImageProtocolKitty):
		return ImageProtocolKitty
	case string(ImageProtocolText), "none", "off":
		return ImageProtocolText
	}

	term := strings.ToLower(lookupEnv("TERM"))
	termProgram := strings.ToLower(lookupEnv("TERM_PROGRAM"))

	// A multiplexer sits between this and the terminal that can draw, and passes
	// graphics through only when it has been told to. It also keeps the outer
	// terminal's own variables — GHOSTTY_RESOURCES_DIR survives into a tmux pane —
	// so those cannot be trusted from inside one.
	if lookupEnv("TMUX") != "" || strings.HasPrefix(term, "screen") ||
		strings.HasPrefix(term, "tmux") || lookupEnv("ZELLIJ") != "" {
		return ImageProtocolText
	}

	// Kitty and Ghostty both draw from a Unicode placeholder, and each says who it
	// is more than one way: Ghostty's TERM is xterm-ghostty unless the reader has
	// set it to something else, which is why its shell variables count too.
	switch {
	case lookupEnv("KITTY_WINDOW_ID") != "", strings.Contains(term, "kitty"), termProgram == "kitty":
		return ImageProtocolKitty
	case lookupEnv("GHOSTTY_RESOURCES_DIR") != "", strings.Contains(term, "ghostty"), termProgram == "ghostty":
		return ImageProtocolKitty
	}

	if strings.HasPrefix(term, "foot") || termProgram == "foot" {
		return ImageProtocolSixel
	}
	return ImageProtocolText
}

func (textImageRenderer) Protocol() ImageProtocol { return ImageProtocolText }

// A terminal that cannot draw an image is told nothing. What stands in for it is
// the caller's business: in a chat that is the filename and its size, which is
// what the line said before images were drawn at all.
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
