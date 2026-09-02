package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/tui"
)

func testImageBytes(t *testing.T, width, height int) []byte {
	t.Helper()

	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})

	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, canvas))
	return encoded.Bytes()
}

// noImages is the renderer a terminal that draws none gets.
type noImages struct{}

func (noImages) Protocol() tui.ImageProtocol { return tui.ImageProtocolText }

func (noImages) Render([]byte, int, int) tui.RenderedImage { return tui.RenderedImage{} }

// drawnImage is a renderer that draws without a terminal, so a test can check what
// the screen does with a picture rather than what Kitty does with one.
type drawnImage struct{ cols, rows int }

func (drawnImage) Protocol() tui.ImageProtocol { return tui.ImageProtocolKitty }

func (d drawnImage) Render(_ []byte, id, maxCols int) tui.RenderedImage {
	cols := min(d.cols, maxCols)
	rows := make([]string, d.rows)
	for index := range rows {
		rows[index] = strings.Repeat("▒", cols)
	}
	return tui.RenderedImage{
		Content: strings.Join(rows, "\n"),
		Raw:     fmt.Sprintf("<pixels for %d>", id),
		Cols:    cols,
		Rows:    d.rows,
	}
}

// --- Which attachments are pictures ---

func TestImageAttachmentPicksThePicture(t *testing.T) {
	picture := basecamp.CampfireLine{Attachments: []basecamp.CampfireLineAttachment{
		{Filename: "image.png", ContentType: "image/png", DownloadURL: "https://3.basecampapi.com/1/blobs/a/download/image.png"},
	}}
	assert.Equal(t, "https://3.basecampapi.com/1/blobs/a/download/image.png", imageAttachment(picture))

	// A video has a preview on the web, but nothing here can draw one.
	video := basecamp.CampfireLine{Attachments: []basecamp.CampfireLineAttachment{
		{Filename: "clip.mp4", ContentType: "video/mp4", DownloadURL: "https://3.basecampapi.com/1/blobs/b/download/clip.mp4"},
	}}
	assert.Empty(t, imageAttachment(video))

	// A picture with nowhere to read it from is not one.
	assert.Empty(t, imageAttachment(basecamp.CampfireLine{
		Attachments: []basecamp.CampfireLineAttachment{{ContentType: "image/png"}},
	}))
	assert.Empty(t, imageAttachment(basecamp.CampfireLine{}))
}

// --- Where a picture may come from ---

func TestOnlyTheAccountsOwnHostIsRead(t *testing.T) {
	const base = "https://3.basecampapi.com"

	require.NoError(t, onAPIHost("https://3.basecampapi.com/1/blobs/a/download/image.png", base))

	for name, source := range map[string]string{
		"another host":   "https://evil.example/image.png",
		"another scheme": "http://3.basecampapi.com/1/blobs/a/download/image.png",
		"credentials":    "https://someone:secret@3.basecampapi.com/1/blobs/a/download/image.png",
		"relative":       "/1/blobs/a/download/image.png",
		"not a URL":      "https://exa mple.com/\x7f",
	} {
		err := onAPIHost(source, base)
		require.Error(t, err, name)
		assert.ErrorIs(t, err, errImageRefused, name)
	}
}

// --- What arrived has to be a picture ---

func TestOnlyRealImagesAreDrawn(t *testing.T) {
	good := testImageBytes(t, 10, 10)
	require.NoError(t, validImage(good, "image/png"))
	require.NoError(t, validImage(good, ""), "a response with no type is judged by its bytes")

	for name, test := range map[string]struct {
		data        []byte
		contentType string
	}{
		"not an image at all": {[]byte("<html>nope</html>"), "image/png"},
		"declared as a page":  {good, "text/html"},
		"an invalid type":     {good, "image/"},
		"empty":               {nil, "image/png"},
	} {
		assert.Error(t, validImage(test.data, test.contentType), name)
	}
}

// --- What one screen will spend ---

// A reader walking a long way back through a busy chat stops fetching rather than
// filling their memory with it.
func TestTheImageBudgetStopsAtItsCount(t *testing.T) {
	budget := newImageBudget()
	data := testImageBytes(t, 4, 4)

	asked := 0
	read := func(context.Context, string, int64) ([]byte, error) {
		asked++
		return data, nil
	}

	wanted := make([]string, 0, maxImagesPerScreen*2)
	for index := range maxImagesPerScreen * 2 {
		wanted = append(wanted, fmt.Sprintf("https://x/%d.png", index))
	}

	got := budget.fetch(context.Background(), read, wanted)
	assert.Equal(t, maxImagesPerScreen, asked)
	assert.Len(t, got, maxImagesPerScreen)
	assert.True(t, budget.spent())
}

// The bytes are counted too, so a handful of large pictures costs what a lot of
// small ones does.
func TestTheImageBudgetStopsAtItsBytes(t *testing.T) {
	budget := newImageBudget()
	budget.remainingBytes = 100

	read := func(_ context.Context, _ string, maxBytes int64) ([]byte, error) {
		assert.LessOrEqual(t, maxBytes, int64(100), "the reader was not told what was left")
		return bytes.Repeat([]byte("x"), 60), nil
	}

	got := budget.fetch(context.Background(), read, []string{
		"https://x/1.png",
		"https://x/2.png",
	})

	assert.Len(t, got, 1, "the second picture did not fit and was kept anyway")
	assert.Contains(t, got, "https://x/1.png")
	assert.Equal(t, int64(40), budget.remainingBytes, "the first picture was not charged for")
}

// The same picture twice is one request.
func TestTheSamePictureIsReadOnce(t *testing.T) {
	budget := newImageBudget()
	asked := 0
	read := func(context.Context, string, int64) ([]byte, error) {
		asked++
		return testImageBytes(t, 4, 4), nil
	}

	budget.fetch(context.Background(), read, []string{
		"https://x/1.png",
		"https://x/1.png#again",
	})

	assert.Equal(t, 1, asked)
}

// A refusal is not a request, so it costs nothing: a run of URLs pointing somewhere
// else cannot use up the count and leave the real pictures unread.
func TestARefusedURLCostsNothing(t *testing.T) {
	budget := newImageBudget()
	read := func(_ context.Context, source string, _ int64) ([]byte, error) {
		if strings.Contains(source, "elsewhere") {
			return nil, fmt.Errorf("%w: not ours", errImageRefused)
		}
		return testImageBytes(t, 4, 4), nil
	}

	wanted := make([]string, 0, maxImagesPerScreen+1)
	for index := range maxImagesPerScreen {
		wanted = append(wanted, fmt.Sprintf("https://elsewhere/%d.png", index))
	}
	wanted = append(wanted, "https://x/real.png")

	got := budget.fetch(context.Background(), read, wanted)
	assert.Len(t, got, 1)
	assert.Contains(t, got, "https://x/real.png")
}

// A read that fails is still a request the conversation caused, and what is on
// screen says what the file was called either way.
func TestAFailedReadIsStillCharged(t *testing.T) {
	budget := newImageBudget()
	read := func(context.Context, string, int64) ([]byte, error) {
		return nil, errors.New("no route to host")
	}

	got := budget.fetch(context.Background(), read, []string{"https://x/1.png"})
	assert.Empty(t, got)
	assert.Equal(t, maxImagesPerScreen-1, budget.remaining)
}

// --- On the screen ---

// withPicture is a chat holding one upload line, drawn: the pixels sent and the
// cells put on the line, in that order, which is the order the screen does it in.
func withPicture(t *testing.T, cols, rows, width int) (model, *chatScreen) {
	t.Helper()

	m, c := openChat(t, width, 30)
	c.images = drawnImage{cols: cols, rows: rows}
	c.lines = append(c.lines, chatLine{
		id: 500, who: "Rob Zolkos", body: "📎 image.png (192.7kb)",
		at: testNow, imageURL: "https://3.basecampapi.com/1/blobs/a/download/image.png",
	})
	m.relayout()

	cmd := c.drawImages(map[string][]byte{"https://3.basecampapi.com/1/blobs/a/download/image.png": testImageBytes(t, cols*10, rows*20)})
	require.NotNil(t, cmd, "the pixels were never sent to the terminal")
	return place(t, m, c), c
}

// place runs the second half of drawing, the way the sequence the screen returns
// does: the terminal has the pixels, so the cells can go on their lines.
func place(t *testing.T, m model, c *chatScreen) model {
	t.Helper()

	drawn := map[int64]tui.RenderedImage{}
	for _, line := range c.lines {
		if line.imageData != nil {
			drawn[line.id] = c.images.Render(line.imageData, 1, c.bodyWidth())
		}
	}
	updated, _ := m.Update(placeImages(drawn)())
	return updated.(model)
}

// The pixels go out before the cells that point at them. A cell names a row and a
// column of an image the terminal has been given; one that names an image it has
// never heard of draws nothing, and nothing changes after the frame to make it look
// again.
func TestThePixelsGoOutBeforeTheCells(t *testing.T) {
	m, c := openChat(t, 96, 30)
	c.images = drawnImage{cols: 20, rows: 3}
	c.lines = append(c.lines, chatLine{
		id: 500, who: "Rob Zolkos", body: "📎 image.png", at: testNow,
		imageURL: "https://3.basecampapi.com/1/blobs/a/download/image.png",
	})
	m.relayout()

	require.NotNil(t, c.drawImages(map[string][]byte{"https://3.basecampapi.com/1/blobs/a/download/image.png": testImageBytes(t, 200, 100)}))
	assert.Empty(t, c.lines[len(c.lines)-1].image.Content,
		"the cells went on screen in the same frame the pixels were sent in")
	assert.NotContains(t, ansi.Strip(c.View()), "▒")

	m = place(t, m, c)
	assert.Contains(t, ansi.Strip(c.View()), "▒", "the cells never arrived")
}

// A picture leads and its filename sits under it, the way the web puts a caption
// under the card.
func TestAPictureLeadsItsFilename(t *testing.T) {
	_, c := withPicture(t, 20, 3, 96)

	rendered := ansi.Strip(c.View())
	cells := strings.Index(rendered, strings.Repeat("▒", 20))
	filename := strings.Index(rendered, "image.png")
	require.Positive(t, cells, "the picture was not drawn")
	assert.Less(t, cells, filename, "the filename was drawn above the picture")
}

// The cells are what the terminal matches the image against, so a column narrower
// than the picture leaves it out rather than showing part of one.
func TestAPictureTooWideForTheColumnIsLeftOut(t *testing.T) {
	m, c := withPicture(t, 60, 3, 96)
	assert.Contains(t, ansi.Strip(c.View()), "▒", "the picture was not drawn at a width that fits")

	_ = resize(t, m, 40, 30)
	rendered := ansi.Strip(c.View())
	assert.NotContains(t, rendered, "▒", "a cut-down picture was drawn")
	assert.Contains(t, rendered, "image.png", "the filename went with it")
}

// A terminal that cannot draw a picture is never asked to read one, and is never
// told about it either. The filename is a whole message; a reader is not made to
// hear about what their terminal cannot do.
func TestATerminalThatCannotDrawIsLeftAlone(t *testing.T) {
	_, c := openChat(t, 96, 30)
	c.images = noImages{}
	c.lines = append(c.lines, chatLine{
		id: 500, who: "Rob Zolkos", body: "📎 image.png (192.7kb)", at: testNow,
		imageURL: "https://3.basecampapi.com/1/blobs/a/download/image.png",
	})

	assert.Nil(t, c.readImages(), "a picture was read for a terminal that cannot draw it")
	assert.Nil(t, c.lines[len(c.lines)-1].imageData)

	rendered := ansi.Strip(c.View())
	assert.Contains(t, rendered, "image.png (192.7kb)")
	assert.NotContains(t, rendered, "can't", "the reader was told off about their terminal")
}

// A picture already read is not read again.
func TestAPictureIsReadOnce(t *testing.T) {
	_, c := openChat(t, 96, 30)
	c.images = drawnImage{cols: 10, rows: 2}
	c.lines = append(c.lines, chatLine{
		id: 500, at: testNow, imageURL: "https://3.basecampapi.com/1/blobs/a/download/image.png",
	})

	require.Len(t, wantedImages(c.lines), 1)

	// Charged when the data lands, not when the cells do: the pixels are still on
	// their way out at this point, and nothing should ask for them again meanwhile.
	c.drawImages(map[string][]byte{"https://3.basecampapi.com/1/blobs/a/download/image.png": testImageBytes(t, 40, 40)})
	assert.Empty(t, wantedImages(c.lines), "a picture already read was asked for again")
	assert.Nil(t, c.readImages())
}

// Nothing left to spend, and no read is started at all.
func TestASpentBudgetStartsNoRead(t *testing.T) {
	_, c := openChat(t, 96, 30)
	c.images = drawnImage{cols: 10, rows: 2}
	c.budget.remaining = 0
	c.lines = append(c.lines, chatLine{
		id: 500, at: testNow, imageURL: "https://3.basecampapi.com/1/blobs/a/download/image.png",
	})

	assert.Nil(t, c.readImages())
}

// A page of lines asks for the pictures it brought.
func TestAPageAsksForItsPictures(t *testing.T) {
	m := resize(t, newTestModel(t), 96, 30)
	c := newChat(m.ctx, testChatTool())
	c.now = func() time.Time { return testNow }
	c.images = drawnImage{cols: 10, rows: 2}
	m.push(c)

	cmd, _ := c.Update(chatPageMsg{page: 1, lines: []chatLine{
		{id: 1, who: "Rob Zolkos", body: "📎 image.png", at: testNow,
			imageURL: "https://3.basecampapi.com/1/blobs/a/download/image.png"},
	}})

	require.NotNil(t, cmd, "a page with a picture in it read nothing")
	require.Len(t, wantedImages(c.lines), 1)
}
