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

// --- Asking the terminal ---

// A terminal that draws Kitty graphics answers the query with an OK for the id it
// was asked about.
func TestATerminalThatAnswersOKDrawsPictures(t *testing.T) {
	draws, answered := readImageAnswer([]byte("\x1b_Gi=4242;OK\x1b\\\x1b[?62;c"))

	assert.True(t, answered)
	assert.True(t, draws)
}

// One that does not answers nothing to the query, and the device attributes request
// behind it comes back on its own. That is the no — and it arrives in a round trip
// rather than at the end of the deadline.
func TestATerminalThatAnswersOnlyItsAttributesDrawsNone(t *testing.T) {
	for name, reply := range map[string]string{
		"xterm":      "\x1b[?1;2c",
		"vt220":      "\x1b[?62;1;2;6;8;9;15c",
		"bare":       "\x1b[?c",
		"after keys": "q\x1b[?6c",
	} {
		draws, answered := readImageAnswer([]byte(reply))
		assert.True(t, answered, name)
		assert.False(t, draws, name)
	}
}

// Silence is not a no: it is a terminal that was not listening, and nothing is
// decided on it.
func TestATerminalThatSaysNothingDecidesNothing(t *testing.T) {
	for name, reply := range map[string]string{
		"empty":                             "",
		"unrelated keys":                    "abc",
		"a cursor report":                   "\x1b[12;40R",
		"a half-finished attributes report": "\x1b[?62;1",
	} {
		_, answered := readImageAnswer([]byte(reply))
		assert.False(t, answered, name)
	}
}

// The query is the one Kitty documents, and it never displays anything: a=q asks,
// and the payload is a single transparent pixel.
func TestTheProbeAsksAndShowsNothing(t *testing.T) {
	request := imageProbeRequest()

	assert.Contains(t, request, "\x1b_Ga=q,i=4242,s=1,v=1,f=24,t=d;AAAA\x1b\\")
	assert.Contains(t, request, deviceAttrs)
	assert.NotContains(t, request, "a=T", "the probe would have displayed the pixel")
}

// A relay that forwards the question but not the pixels is the one thing asking
// cannot settle: the terminal behind it answers OK, the image data never arrives,
// and the cells are drawn into empty space. Both of these were seen doing it.
func TestARelayThatEatsPicturesIsNotAsked(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"herdr": {"HERDR_ENV": "1"},
		"tmux":  {"TMUX": "/tmp/tmux-1000/default,1,0"},
	} {
		assert.True(t, insideARelayThatEatsPictures(func(key string) string { return env[key] }), name)
	}

	// Nothing else is on the list. A relay is not assumed from a variable that
	// merely sounds like one.
	for name, env := range map[string]map[string]string{
		"a plain terminal": {"TERM": "xterm-256color"},
		"ghostty":          {"TERM_PROGRAM": "ghostty", "GHOSTTY_RESOURCES_DIR": "/usr/share/ghostty"},
		"kitty":            {"KITTY_WINDOW_ID": "1"},
	} {
		assert.False(t, insideARelayThatEatsPictures(func(key string) string { return env[key] }), name)
	}
}

// --- Which renderer that gets you ---

// Nothing is guessed from the environment: a terminal's own variables outlive it,
// and inside tmux or herdr they describe a terminal that is no longer on the other
// end. Until the terminal answers, a picture is its filename.
func TestNoAnswerMeansNoPictures(t *testing.T) {
	t.Setenv(ImageProtocolVar, "")
	drawsImages.Store(false)

	assert.Equal(t, ImageProtocolText, NewImageRenderer().Protocol())
}

func TestAnAnswerOfYesDrawsPictures(t *testing.T) {
	t.Setenv(ImageProtocolVar, "")
	drawsImages.Store(true)
	defer drawsImages.Store(false)

	assert.Equal(t, ImageProtocolKitty, NewImageRenderer().Protocol())
}

// No probe is going to be right about every terminal, so there is a way to say
// outright — in both directions.
func TestTheImageProtocolCanBeSaidOutright(t *testing.T) {
	drawsImages.Store(false)

	t.Setenv(ImageProtocolVar, "kitty")
	assert.Equal(t, ImageProtocolKitty, NewImageRenderer().Protocol(), "a terminal that would not say so")

	drawsImages.Store(true)
	defer drawsImages.Store(false)

	for _, off := range []string{"text", "none", "off", "TEXT"} {
		t.Setenv(ImageProtocolVar, off)
		assert.Equal(t, ImageProtocolText, NewImageRenderer().Protocol(), off)
	}

	t.Setenv(ImageProtocolVar, "yes please")
	assert.Equal(t, ImageProtocolKitty, NewImageRenderer().Protocol(), "a value nobody meant")
}

// A terminal that cannot draw a picture is handed nothing and told nothing. What
// stands in its place is the caller's business.
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
