package workspace

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	testPreviewURL  = "https://preview.app.basecamp.com/1/blobs/a/previews/full"
	testDownloadURL = "https://3.basecampapi.com/1/blobs/a/download/shot.png"
)

func testPicturePost() message {
	return message{
		id:      20,
		subject: "Pricing states",
		author:  person{name: "Andy S."},
		at:      testNow,
		body:    "Here's the plan.\n\n![Adminland banner](" + testPreviewURL + ")\n\nAnd the rest.",
		images:  map[string]string{testPreviewURL: testDownloadURL},
	}
}

// placePictures is the second half of drawing, which tea.Sequence would do in a
// running program: the terminal has the pixels, so the cells that point at them
// go on screen.
func placePictures(t *testing.T, m model, post *messageScreen, sources ...string) model {
	t.Helper()

	drawn := map[string]tui.RenderedImage{}
	for _, source := range sources {
		drawn[source] = post.shown.renderer.Render(testImageBytes(t, 40, 20), 1, post.width)
	}
	updated, _ := m.Update(placeMessageImages(drawn)())
	return updated.(model)
}

// openPicturePost is a message screen whose terminal draws pictures, with the
// read not yet started.
func openPicturePost(t *testing.T, cols, rows, width int) (model, *messageScreen) {
	t.Helper()

	m := resize(t, newTestModel(t), width, 40)
	post := newMessage(m.ctx, testPicturePost())
	post.now = func() time.Time { return testNow }
	post.shown.renderer = drawnImage{cols: cols, rows: rows}
	m.push(post)
	m.relayout()
	return m, post
}

// --- Splitting the body ---

// The prose either side of a picture stays prose, and the picture becomes its own
// part in the order it appeared.
func TestSplitBodySeparatesProseFromPictures(t *testing.T) {
	parts := splitBody(testPicturePost().body)

	require.Len(t, parts, 3)
	assert.Contains(t, parts[0].text, "Here's the plan.")
	assert.False(t, parts[0].isImage())

	assert.True(t, parts[1].isImage())
	assert.Equal(t, "Adminland banner", parts[1].alt)
	assert.Equal(t, testPreviewURL, parts[1].url)

	assert.Contains(t, parts[2].text, "And the rest.")
}

func TestSplitBodyWithoutPictures(t *testing.T) {
	parts := splitBody("Just words.")

	require.Len(t, parts, 1)
	assert.False(t, parts[0].isImage())

	assert.Empty(t, splitBody(""))
	assert.Empty(t, splitBody("   \n\n "))
}

// A body that is nothing but a picture has no prose to keep.
func TestSplitBodyOfOnlyAPicture(t *testing.T) {
	parts := splitBody("![shot](" + testPreviewURL + ")")

	require.Len(t, parts, 1)
	assert.True(t, parts[0].isImage())
}

// --- Where a picture is read from ---

// The body points at the preview host, which is not one this reads from. The
// same attachment carries a download address on the API host, and that is the one
// used.
func TestImageSourcesMapPreviewsToDownloads(t *testing.T) {
	sources := imageSources([]basecamp.RichTextAttachment{
		{ContentType: "image/png", PreviewURL: testPreviewURL, DownloadURL: testDownloadURL},
		{ContentType: "application/pdf", PreviewURL: "https://preview/x", DownloadURL: "https://api/x.pdf"},
		{ContentType: "image/png", PreviewURL: "https://preview/y", DownloadURL: ""},
	})

	assert.Equal(t, testDownloadURL, sources[testPreviewURL])
	assert.Equal(t, testDownloadURL, sources[testDownloadURL], "the download address resolves to itself")
	assert.NotContains(t, sources, "https://preview/x", "a PDF is not a picture")
	assert.NotContains(t, sources, "https://preview/y", "a picture with nowhere to read it from is not one")
}

// --- Reading them one at a time ---

// A message can carry twenty screenshots, so they are read one by one and the
// screen asks for the next as each lands.
func TestPicturesAreReadOneAtATime(t *testing.T) {
	_, post := openPicturePost(t, 4, 2, 96)

	require.NotNil(t, post.shown.read(post.words), "no read was started")
	assert.Equal(t, []string{testDownloadURL}, post.shown.queue)

	// The one in flight is still counted as coming, so its row says so.
	assert.True(t, post.shown.coming(post.words, post.words.parts[1]))
}

// A terminal that cannot draw pictures is never asked to read one.
func TestATerminalThatDrawsNoneReadsNone(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := newMessage(m.ctx, testPicturePost())
	post.shown.renderer = noImages{}
	m.push(post)

	assert.Nil(t, post.shown.read(post.words))
	assert.Empty(t, post.shown.queue)
}

// --- The wait before drawing ---

// Pictures that arrive during the wait are drawn together, so a message full of
// screenshots redraws once rather than once per picture.
func TestArrivalsAreDrawnTogether(t *testing.T) {
	_, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)

	cmd := post.shown.arrivedOne(messageImageMsg{source: testDownloadURL, data: testImageBytes(t, 40, 20)})
	require.NotNil(t, cmd)
	assert.True(t, post.shown.waiting, "no wait was armed")
	assert.Empty(t, post.shown.picture(post.words, post.words.parts[1], post.width), "the picture was drawn before the wait was up")

	// A second arrival inside the wait arms no second wait.
	post.shown.arrived["other"] = testImageBytes(t, 4, 4)
	post.shown.arrivedOne(messageImageMsg{source: "other"})
	assert.True(t, post.shown.waiting)
}

// The wait ending sends the pixels and only then puts the cells on screen: cells
// drawn in the same frame as the pixels point at a picture the terminal does not
// have yet.
func TestTheWaitEndingDrawsWhatArrived(t *testing.T) {
	m, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)
	post.shown.arrivedOne(messageImageMsg{source: testDownloadURL, data: testImageBytes(t, 40, 20)})

	cmd := post.shown.draw(post.words)
	require.NotNil(t, cmd, "the pixels were never sent to the terminal")
	assert.False(t, post.shown.waiting)
	assert.Empty(t, post.shown.arrived, "the same picture would be drawn again")
	assert.Empty(t, post.shown.picture(post.words, post.words.parts[1], post.width), "the cells went on screen in the same frame as the pixels")

	_ = placePictures(t, m, post, testDownloadURL)
	assert.NotEmpty(t, post.shown.picture(post.words, post.words.parts[1], post.width), "the cells never arrived")
	assert.Contains(t, ansi.Strip(post.View()), "▒")
}

// Nothing arrived, nothing to draw.
func TestTheWaitEndingWithNothingDrawsNothing(t *testing.T) {
	_, post := openPicturePost(t, 4, 2, 96)

	assert.Nil(t, post.shown.draw(post.words))
}

// --- What stands where a picture will go ---

// A reader watching a message full of screenshots has to be able to tell a
// picture on its way from a gap.
func TestAPictureOnItsWaySaysSo(t *testing.T) {
	_, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)

	rendered := ansi.Strip(post.View())
	assert.Contains(t, rendered, "Loading Adminland banner…")
	assert.Contains(t, rendered, spinnerFrames[0], "the wait was still rather than turning")
	assert.NotContains(t, rendered, testPreviewURL, "the address was shown as well as the wait")
}

// The throbber turns, so a read that takes a while looks like work rather than a
// hang.
func TestTheThrobberTurnsWhilePicturesAreRead(t *testing.T) {
	_, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)
	require.True(t, post.shown.spinning, "no throbber was armed")

	assert.Contains(t, ansi.Strip(post.View()), spinnerFrames[0])
	require.NotNil(t, mustTake(post.shown.update(imageSpinMsg{}, post.words)), "the throbber stopped while a picture was still coming")
	assert.Contains(t, ansi.Strip(post.View()), spinnerFrames[1])
}

// Nothing left to wait for and the throbber stops, so an idle screen wakes up for
// nothing.
func TestTheThrobberStopsWhenNothingIsComing(t *testing.T) {
	_, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)
	post.shown.arrivedOne(messageImageMsg{source: testDownloadURL, data: testImageBytes(t, 40, 20)})
	post.shown.draw(post.words)

	assert.Nil(t, mustTake(post.shown.update(imageSpinMsg{}, post.words)))
	assert.False(t, post.shown.spinning)
}

// A picture with no caption still says something.
func TestAPictureWithNoCaptionStillSaysItIsComing(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	post := newMessage(m.ctx, message{
		id: 1, subject: "Shot", body: "![](" + testPreviewURL + ")",
		images: map[string]string{testPreviewURL: testDownloadURL},
	})
	post.shown.renderer = drawnImage{cols: 4, rows: 2}
	m.push(post)
	post.shown.read(post.words)
	m.relayout()

	assert.Contains(t, ansi.Strip(post.View()), "Loading image…")
}

// A read that failed leaves the address, which is what the body said before
// pictures were drawn at all.
func TestAPictureThatNeverArrivedFallsBackToItsAddress(t *testing.T) {
	m, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)
	post.shown.arrivedOne(messageImageMsg{source: testDownloadURL})
	m.relayout()

	rendered := ansi.Strip(post.View())
	assert.NotContains(t, rendered, "Loading")
	assert.Contains(t, rendered, "Adminland banner")
}

// The caption goes under the picture, where the web puts it.
func TestACaptionSitsUnderThePicture(t *testing.T) {
	m, post := openPicturePost(t, 4, 2, 96)
	post.shown.read(post.words)
	post.shown.arrivedOne(messageImageMsg{source: testDownloadURL, data: testImageBytes(t, 40, 20)})
	post.shown.draw(post.words)
	_ = placePictures(t, m, post, testDownloadURL)

	lines := strings.Split(ansi.Strip(post.View()), "\n")
	for index, line := range lines {
		if strings.Contains(line, "▒") && index+1 < len(lines) {
			continue
		}
		if strings.Contains(line, "Adminland banner") {
			assert.Contains(t, lines[index-1], "▒", "the caption is not under the picture")
			return
		}
	}
	t.Fatal("no caption was drawn")
}

// A window narrowed after a picture was sized leaves it out rather than showing
// part of one: the cells are what the terminal matches the image against.
func TestAPictureInAMessageTooWideForTheColumnIsLeftOut(t *testing.T) {
	m, post := openPicturePost(t, 80, 4, 96)
	post.shown.read(post.words)
	post.shown.arrivedOne(messageImageMsg{source: testDownloadURL, data: testImageBytes(t, 800, 40)})
	post.shown.draw(post.words)
	_ = placePictures(t, m, post, testDownloadURL)
	require.NotEmpty(t, post.shown.picture(post.words, post.words.parts[1], post.width))

	post.Resize(20, 40)
	assert.Empty(t, post.shown.picture(post.words, post.words.parts[1], post.width))
}

// mustTake unwraps what a component answered, for a test driving one message
// straight into it rather than through the screen that holds it.
func mustTake(cmd tea.Cmd, took bool) tea.Cmd {
	if !took {
		panic("the component did not take the message")
	}
	return cmd
}
