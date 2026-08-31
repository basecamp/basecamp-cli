package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPNG is a picture of a given size, so a test can talk about cells without
// carrying a fixture file around.
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			canvas.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}

	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, canvas))
	return encoded.Bytes()
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, canvas, nil))
	return encoded.Bytes()
}

// --- Which terminal can draw ---

func TestDetectImageCapability(t *testing.T) {
	for name, test := range map[string]struct {
		env  map[string]string
		want ImageProtocol
	}{
		"kitty by window id": {map[string]string{"KITTY_WINDOW_ID": "1"}, ImageProtocolKitty},
		"kitty by term":      {map[string]string{"TERM": "xterm-kitty"}, ImageProtocolKitty},
		"kitty by program":   {map[string]string{"TERM_PROGRAM": "kitty"}, ImageProtocolKitty},

		// Ghostty's own TERM is xterm-ghostty, and its shell variables are there
		// whatever the reader has set TERM to.
		"ghostty by program":   {map[string]string{"TERM_PROGRAM": "ghostty"}, ImageProtocolKitty},
		"ghostty by term":      {map[string]string{"TERM": "xterm-ghostty"}, ImageProtocolKitty},
		"ghostty by resources": {map[string]string{"GHOSTTY_RESOURCES_DIR": "/usr/share/ghostty", "TERM": "xterm-256color"}, ImageProtocolKitty},

		"foot":           {map[string]string{"TERM": "foot-extra"}, ImageProtocolSixel},
		"anything else":  {map[string]string{"TERM": "xterm-256color"}, ImageProtocolText},
		"nothing at all": {map[string]string{}, ImageProtocolText},
	} {
		got := DetectImageCapability(func(key string) string { return test.env[key] })
		assert.Equal(t, test.want, got, name)
	}
}

// No set of signals is going to be right about every terminal, so there is a way
// to say outright — in both directions.
func TestTheImageProtocolCanBeSaidOutright(t *testing.T) {
	for name, test := range map[string]struct {
		env  map[string]string
		want ImageProtocol
	}{
		"forced on in a plain terminal": {
			map[string]string{ImageProtocolVar: "kitty", "TERM": "xterm-256color"}, ImageProtocolKitty},
		"forced on inside tmux": {
			map[string]string{ImageProtocolVar: "kitty", "TMUX": "/tmp/tmux-1000/default,1,0"}, ImageProtocolKitty},
		"forced off in ghostty": {
			map[string]string{ImageProtocolVar: "text", "TERM_PROGRAM": "ghostty"}, ImageProtocolText},
		"forced off, spelled none": {
			map[string]string{ImageProtocolVar: "none", "KITTY_WINDOW_ID": "1"}, ImageProtocolText},
		"a value nobody meant leaves the guessing alone": {
			map[string]string{ImageProtocolVar: "yes please", "TERM_PROGRAM": "ghostty"}, ImageProtocolKitty},
	} {
		got := DetectImageCapability(func(key string) string { return test.env[key] })
		assert.Equal(t, test.want, got, name)
	}
}

// A multiplexer passes graphics through only when it has been told to, and keeps
// the outer terminal's own variables either way — GHOSTTY_RESOURCES_DIR is there in
// a tmux pane. Drawing placeholders into one shows the cells for what they are made
// of: a private-use character and two combining marks apiece, a screen of accent
// soup where the picture should be.
func TestAMultiplexerDrawsNoPictures(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"tmux by variable": {"TMUX": "/tmp/tmux-1000/default,1,0", "TERM_PROGRAM": "ghostty",
			"GHOSTTY_RESOURCES_DIR": "/usr/share/ghostty"},
		"tmux by term":   {"TERM": "tmux-256color", "GHOSTTY_RESOURCES_DIR": "/usr/share/ghostty"},
		"screen by term": {"TERM": "screen.xterm-256color", "KITTY_WINDOW_ID": "1"},
		"zellij":         {"ZELLIJ": "0", "TERM_PROGRAM": "ghostty"},
	} {
		got := DetectImageCapability(func(key string) string { return env[key] })
		assert.Equal(t, ImageProtocolText, got, name)
	}
}

// Sixel is detected and then not used: the sequences are cursor-positioned, and
// Bubble Tea redraws the grid after them, so the image would land wherever the
// cursor was and survive one frame.
func TestASixelTerminalGetsTheWords(t *testing.T) {
	renderer := SelectImageRenderer(func(key string) string {
		if key == "TERM" {
			return "foot"
		}
		return ""
	})

	assert.Equal(t, ImageProtocolText, renderer.Protocol())
}

// A terminal that cannot draw a picture is told nothing at all, and what stands in
// for it is the caller's business.
func TestATerminalThatCannotDrawGetsNothing(t *testing.T) {
	drawn := textImageRenderer{}.Render(testPNG(t, 100, 50), 1, 60)

	assert.Empty(t, drawn.Content)
	assert.Empty(t, drawn.Raw)
	assert.Zero(t, drawn.Cols)
}

// --- What Kitty is sent ---

// An image is two things: the cells that stand where it goes, and the pixels.
func TestKittyDrawsCellsAndSendsPixels(t *testing.T) {
	drawn := kittyImageRenderer{}.Render(testPNG(t, 200, 100), 0x010203, 60)

	require.NotEmpty(t, drawn.Content)
	assert.Positive(t, drawn.Cols)
	assert.Positive(t, drawn.Rows)
	assert.Equal(t, drawn.Rows, len(strings.Split(drawn.Content, "\n")))

	// Every cell is the placeholder rune, and the id rides in the foreground color.
	assert.Contains(t, drawn.Content, string(placeholder))
	assert.Contains(t, drawn.Content, "\033[38;2;1;2;3m")

	// The pixels go up as PNG, then a virtual placement points the cells at them.
	assert.Contains(t, drawn.Raw, "\033_Ga=t,t=d,f=100,q=2,i=66051,")
	assert.Contains(t, drawn.Raw, "\033_Ga=p,U=1,i=66051,")
}

// A cell carries which row and column of the image it is, as combining marks on the
// placeholder. Without them the terminal has no idea where in the picture a cell
// sits.
func TestEveryCellSaysWhereItIs(t *testing.T) {
	content := renderImagePlaceholder(0x010101, 3, 2)
	rows := strings.Split(content, "\n")
	require.Len(t, rows, 2)

	for row := range rows {
		for col := range 3 {
			want := string(placeholder) + string(diacritics[row]) + string(diacritics[col])
			assert.Contains(t, rows[row], want, "row %d column %d", row, col)
		}
	}
}

// An id no terminal can hold is not drawn: a cell whose color says nothing points
// at nothing.
func TestAnImageWithoutAnIDIsNotDrawn(t *testing.T) {
	assert.Empty(t, renderImagePlaceholder(noImageID, 4, 4))
	assert.Empty(t, renderImagePlaceholder(lastImageID+1, 4, 4))
	assert.Empty(t, kittyUploadAndPlace(testPNG(t, 10, 10), noImageID, 4, 4))
}

// Ids are handed out once. Reusing one replaces the image the terminal holds under
// it while the old placement is still on screen, which draws the new image clipped
// into the old one's cells.
func TestImageIDsAreNeverReused(t *testing.T) {
	seen := map[int]struct{}{}
	for range 500 {
		id := NextImageID()
		require.NotContains(t, seen, id)
		seen[id] = struct{}{}

		// No byte of the color may be zero: some terminals read rgb(0,0,1) as a
		// palette index and lose the image the cell belongs to.
		for _, shift := range []int{0, 8, 16} {
			assert.NotZero(t, (id>>shift)&0xFF, "id %06x has a zero byte", id)
		}
		assert.GreaterOrEqual(t, id, firstImageID)
		assert.LessOrEqual(t, id, lastImageID)
	}
}

// --- How big it is drawn ---

// The terminal stretches an image over the cells it is given, so a small picture in
// a lot of cells comes out smeared. It never gets more cells than its pixels cover.
func TestASmallImageIsNotBlownUp(t *testing.T) {
	cols, rows := imageDimensions(testPNG(t, 20, 20), 60)

	assert.Equal(t, 2, cols, "a 20px image took more than two cells")
	assert.Positive(t, rows)
}

// A big one is cut to the room it was given, and keeps its shape.
func TestABigImageFitsTheColumn(t *testing.T) {
	cols, rows := imageDimensions(testPNG(t, 2330, 1308), 40)

	assert.Equal(t, 40, cols)
	assert.LessOrEqual(t, rows, maxImageRows)
	// 2330x1308 is about 16:9, and a cell is twice as tall as it is wide.
	assert.Equal(t, 11, rows)
}

// A tall image gives up columns rather than running off the bottom of the screen.
func TestATallImageIsBounded(t *testing.T) {
	cols, rows := imageDimensions(testPNG(t, 400, 4000), 60)

	assert.Equal(t, maxImageRows, rows)
	assert.Less(t, cols, 40)
	assert.Positive(t, cols)
}

// Something the decoders do not know still gets a shape, because a gap where an
// image was tells the reader nothing.
func TestAnUnreadableImageStillGetsCells(t *testing.T) {
	cols, rows := imageDimensions([]byte("RIFF....WEBPVP8 "), 60)

	assert.Positive(t, cols)
	assert.Positive(t, rows)
}

// The upload declares PNG, so anything else is re-encoded: a JPEG sent as-is is
// dropped by the terminal and the message shows an empty gap where its image was.
func TestAnythingNotAPNGIsReEncoded(t *testing.T) {
	encoded := pngEncoded(testJPEG(t, 40, 40))

	assert.True(t, bytes.HasPrefix(encoded, []byte("\x89PNG\r\n\x1a\n")), "the JPEG was sent as it arrived")

	// A PNG is passed through untouched rather than recompressed.
	original := testPNG(t, 40, 40)
	assert.Equal(t, original, pngEncoded(original))
}
