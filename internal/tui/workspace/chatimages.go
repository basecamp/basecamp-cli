package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder for image.DecodeConfig
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
)

// A picture in a chat is worth fetching, and every one of them is somebody else's
// file arriving over a wire. The limits below are what the screen will and will not
// do about that, as literal numbers a reader can quote back:
//
// how many images one screen asks for, how many bytes they may add up to, how big
// one of them may be, how many pixels it may claim to have, and how long the whole
// fetch may take. Ported from hey-cli's image budget, with its origin rule swapped
// for this API's: a URL is fetched only from the account's own API host.
const (
	// maxImagesPerScreen is how many image requests one chat makes. A page is
	// fifteen lines and a screen holds a few pages, so this is a couple of screens
	// worth of screenshots.
	maxImagesPerScreen = 12

	// maxImageBytesPerScreen is how many bytes of image data one chat keeps in all.
	// The bytes are kept rather than dropped so an image can be redrawn at a new
	// size without asking for it again.
	maxImageBytesPerScreen int64 = 24 << 20

	// maxImageBytes is how big one image may be.
	maxImageBytes int64 = 8 << 20

	// maxImagePixels is how many pixels one image may claim. A file can be small
	// and still describe a picture too big to decode.
	maxImagePixels int64 = 100 * 1000 * 1000

	// imageFetchDeadline is how long one round of fetching may take in all.
	imageFetchDeadline = 20 * time.Second
)

// errImageRefused is the answer for a URL that is not requested at all — malformed,
// carrying credentials, or from an origin that is not the account's. No request was
// made, which is what a budget counting requests needs to know.
var errImageRefused = errors.New("image URL refused")

// wantedImage is one picture worth fetching: the line it belongs to, and where it
// comes from.
type wantedImage struct {
	line int64
	url  string
}

// chatImagesMsg is the image data that arrived, by the line it belongs to. A line
// whose fetch failed is simply not in it: the message already says what the file
// was called, which is what it said before images were drawn at all.
type chatImagesMsg struct {
	images map[int64][]byte
}

// wantedImages picks the pictures out of a page of lines. An upload line carries
// one attachment, so the first image on a line is its image.
func wantedImages(lines []chatLine) []wantedImage {
	wanted := make([]wantedImage, 0, len(lines))
	for _, line := range lines {
		if line.imageURL != "" && line.image.Content == "" && line.imageData == nil {
			wanted = append(wanted, wantedImage{line: line.id, url: line.imageURL})
		}
	}
	return wanted
}

// imageAttachment is the download address of the first picture attached to a line,
// and empty when nothing about the line is a picture.
//
// The download URL is used rather than the preview one: a preview is served from a
// host of its own, and the download goes through the API, which is the one origin
// this trusts. It costs the full-size file, which imageDimensions then draws no
// larger than the cells justify.
func imageAttachment(line basecamp.CampfireLine) string {
	for _, file := range line.Attachments {
		if strings.HasPrefix(strings.ToLower(file.ContentType), "image/") && file.DownloadURL != "" {
			return file.DownloadURL
		}
	}
	return ""
}

// imageBudget is what one chat screen may spend on pictures. One per screen, so a
// reader walking a long way back through a busy chat stops fetching rather than
// filling their memory with it.
type imageBudget struct {
	remaining      int
	remainingBytes int64
	seen           map[string]struct{}
}

func newImageBudget() *imageBudget {
	return &imageBudget{
		remaining:      maxImagesPerScreen,
		remainingBytes: maxImageBytesPerScreen,
		seen:           map[string]struct{}{},
	}
}

// spent reports whether there is nothing left to spend, so a caller can skip the
// command rather than start one that will do nothing.
func (b *imageBudget) spent() bool {
	return b == nil || b.remaining <= 0 || b.remainingBytes <= 0
}

// admit reports whether a URL is one the budget will request: not asked for before.
// A fragment never goes on the wire, so two URLs differing only there are one
// request and one picture.
func (b *imageBudget) admit(source string) bool {
	request, _, _ := strings.Cut(source, "#")
	if _, seen := b.seen[request]; seen {
		return false
	}
	b.seen[request] = struct{}{}
	return true
}

// fetch reads the pictures a screen may show, in the order they were found, within
// what is left of the budget and within the deadline.
//
// Every request made is charged, whatever it answers: the conversation chose the
// URLs, and one that fails is still a request it caused. A refusal is not a
// request, so it costs nothing — a run of URLs pointing somewhere else cannot use
// up the count and leave the real pictures unfetched.
func (b *imageBudget) fetch(ctx context.Context, read imageReader, wanted []wantedImage) map[int64][]byte {
	if read == nil || len(wanted) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, imageFetchDeadline)
	defer cancel()

	fetched := map[int64][]byte{}
	for _, want := range wanted {
		if b.spent() || ctx.Err() != nil {
			break
		}
		if !b.admit(want.url) {
			continue
		}
		b.remaining--

		// The reader is told what is left, so an image that would not fit is
		// stopped on the wire rather than downloaded whole and then discarded.
		data, err := read(ctx, want.url, min(b.remainingBytes, maxImageBytes))
		if errors.Is(err, errImageRefused) {
			b.remaining++
			continue
		}
		if err != nil || len(data) == 0 || int64(len(data)) > b.remainingBytes {
			continue
		}
		b.remainingBytes -= int64(len(data))
		fetched[want.line] = data
	}
	return fetched
}

// imageReader reads one picture, within maxBytes — which is what the caller has
// left to spend on it.
type imageReader func(ctx context.Context, source string, maxBytes int64) ([]byte, error)

// loadChatImages fetches what a page of lines carries, and answers with whatever
// arrived. It answers even with nothing, so the screen can stop waiting.
func loadChatImages(ctx context.Context, app *appctx.App, budget *imageBudget, wanted []wantedImage) tea.Cmd {
	return func() tea.Msg {
		if err := app.RequireAccount(); err != nil {
			return chatImagesMsg{}
		}
		return chatImagesMsg{images: budget.fetch(ctx, accountImageReader(app), wanted)}
	}
}

// accountImageReader reads a picture through the API, which is the only origin it
// will read one from.
//
// The read itself is the SDK's: DownloadURL authenticates the first hop, follows
// the redirect it answers to the signed storage URL, and sends no credentials on
// that second hop — and follows nothing further, so a storage host cannot redirect
// the read somewhere else.
func accountImageReader(app *appctx.App) imageReader {
	return func(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
		if err := onAPIHost(source, app.Config.BaseURL); err != nil {
			return nil, err
		}

		result, err := app.Account().DownloadURL(ctx, source)
		if err != nil {
			return nil, err
		}
		defer result.Body.Close()

		if result.ContentLength > maxBytes {
			return nil, fmt.Errorf("image is larger than the %d byte limit", maxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(result.Body, maxBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("image is larger than the %d byte limit", maxBytes)
		}
		if err := validImage(data, result.ContentType); err != nil {
			return nil, err
		}
		return data, nil
	}
}

// onAPIHost reports whether a URL is one the account's own API serves. The SDK
// rewrites a download URL's host to the configured base before dialing it, so a
// foreign URL would be fetched from the API host under a foreign path rather than
// from the foreign host — which is safe, and confusing. Refusing it here says what
// is happening: this is not our address.
func onAPIHost(source, base string) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return fmt.Errorf("%w: %s is not a URL", errImageRefused, source)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: an image URL must not carry credentials", errImageRefused)
	}
	if !parsed.IsAbs() {
		return fmt.Errorf("%w: %s is not an absolute URL", errImageRefused, source)
	}

	host, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("%w: the API base %s is not a URL", errImageRefused, base)
	}
	if !strings.EqualFold(parsed.Scheme, host.Scheme) || !strings.EqualFold(parsed.Host, host.Host) {
		return fmt.Errorf("%w: %s is not on %s", errImageRefused, parsed.Host, host.Host)
	}
	return nil
}

// validImage reports whether what arrived is a picture a terminal can be given:
// the type it claims, the type its bytes say it is, and a size worth decoding.
func validImage(data []byte, contentType string) error {
	if contentType != "" {
		declared, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return fmt.Errorf("image has an invalid media type: %w", err)
		}
		if !strings.HasPrefix(declared, "image/") && declared != "application/octet-stream" {
			return fmt.Errorf("image response has media type %s", declared)
		}
	}

	switch http.DetectContentType(data) {
	case "image/gif", "image/jpeg", "image/png":
	default:
		return fmt.Errorf("response content is not an image a terminal can draw")
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return errors.New("response content is not a valid image")
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return fmt.Errorf("image is larger than the %d pixel limit", maxImagePixels)
	}
	return nil
}
