package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	// Avatars are served as WebP whatever the request asks for, and neither
	// validImage nor pngEncoded can read one without a decoder registered.
	_ "golang.org/x/image/webp"

	tea "charm.land/bubbletea/v2"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/observability"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

const (
	// avatarHost serves every person's avatar. It is not the account's API host,
	// which is why avatars need a reader of their own — see avatarReader.
	avatarHost = "bc3-production-assets-cdn.basecamp-static.com"

	// An avatar is small and there is one per person on screen, so it is capped
	// well below what a message's own pictures may spend.
	maxAvatarBytes = 512 << 10

	avatarDeadline = 10 * time.Second

	// How many faces one screen will read. A thread is mostly the same few
	// people, and this is the same picture drawn again wherever they appear.
	maxFacesPerScreen = 24
)

// newFaceBudget is what one screen may spend on people's pictures, which is its
// own allowance rather than a share of the body's. See messageScreen.faceBudget.
func newFaceBudget() *imageBudget {
	return &imageBudget{
		remaining:      maxFacesPerScreen,
		remainingBytes: maxFacesPerScreen * maxAvatarBytes,
		seen:           map[string]struct{}{},
	}
}

// avatarClient reads avatars and nothing else. Redirects are refused rather than
// followed: the allow-list in onAvatarHost is checked on the URL this dials, and
// a redirect is how that check gets walked around.
var avatarClient = &http.Client{
	Timeout: avatarDeadline,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("an avatar URL must not redirect")
	},
}

// avatarsMsg is the avatar data that arrived, by the address it came from, and
// every address that was asked for — including the ones that answered nothing,
// so the throbber standing in for them can stop.
//
// A screen's faces and a card's picture answer with different messages even
// though the read is the same. A modal is handed a message before the screen
// under it and keeps what it takes, so one message for both meant a card opened
// while a screen's faces were in flight swallowed them — and they stayed marked
// as coming, so the throbber turned forever and nothing asked again.
type avatarsMsg struct {
	asked   []string
	avatars map[string][]byte
}

// cardFaceMsg is one person's picture, for the card that asked for it.
type cardFaceMsg struct {
	avatar string
	data   []byte
}

// avatarReader reads a person's avatar.
//
// Its own reader rather than accountImageReader, for two reasons. Avatars are on
// a CDN rather than the account's API host, and accountImageReader deliberately
// refuses anything that is not — it fetches through the SDK's DownloadURL, which
// rewrites a URL's host to the configured base, so a foreign address would be
// fetched from the API under a foreign path. And nothing about an avatar needs
// authenticating: the URL carries its own signature, and this is a third-party
// host, so the account's credentials must not go anywhere near it.
//
// What replaces the API host as the boundary is the allow-list below. An avatar
// address comes from the API's own person payload rather than from anything a
// person wrote, which is what makes one host enough.
func avatarReader(app *appctx.App) imageReader {
	return func(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
		if err := onAvatarHost(source); err != nil {
			return nil, err
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		// No Authorization header, and a client of its own with no cookie jar and
		// no redirect following worth speaking of: nothing about this request
		// should be able to carry anything of the reader's to a third party.
		response, err := avatarClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("avatar responded %s", response.Status)
		}
		if response.ContentLength > maxBytes {
			return nil, fmt.Errorf("avatar is larger than the %d byte limit", maxBytes)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("avatar is larger than the %d byte limit", maxBytes)
		}
		if err := validAvatar(data); err != nil {
			return nil, err
		}
		return data, nil
	}
}

// onAvatarHost reports whether a URL is one the avatar CDN serves. Refusing
// anything else is what keeps this reader from being a way to fetch arbitrary
// addresses.
func onAvatarHost(source string) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return fmt.Errorf("%w: %s is not a URL", errImageRefused, source)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: an avatar URL must not carry credentials", errImageRefused)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("%w: an avatar must be served over https", errImageRefused)
	}
	if !strings.EqualFold(parsed.Host, avatarHost) {
		return fmt.Errorf("%w: %s is not %s", errImageRefused, parsed.Host, avatarHost)
	}
	return nil
}

// validAvatar reports whether what arrived is a picture that can be decoded and
// handed to a terminal.
//
// Looser than validImage about the format, because an avatar arrives as WebP,
// which http.DetectContentType does not name — and stricter about the size,
// because an avatar is drawn into four cells and anything enormous is not one.
func validAvatar(data []byte) error {
	if len(data) == 0 {
		return errors.New("avatar response was empty")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return errors.New("avatar response is not a valid image")
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return fmt.Errorf("avatar is larger than the %d pixel limit", maxImagePixels)
	}
	return nil
}

// loadAvatars reads the avatars a screen shows. They go in one command rather
// than one each: they are small, there is one per person rather than one per
// comment, and none of them is what the reader came for.
func loadAvatars(ctx context.Context, app *appctx.App, budget *imageBudget, wanted []string) tea.Cmd {
	return func() tea.Msg {
		arrived := readAvatars(ctx, app, budget, wanted)
		return avatarsMsg{asked: wanted, avatars: arrived}
	}
}

// loadCardFace reads the one picture a card is about.
func loadCardFace(ctx context.Context, app *appctx.App, budget *imageBudget, avatar string) tea.Cmd {
	return func() tea.Msg {
		arrived := readAvatars(ctx, app, budget, []string{avatar})
		return cardFaceMsg{avatar: avatar, data: arrived[avatar]}
	}
}

func readAvatars(ctx context.Context, app *appctx.App, budget *imageBudget, wanted []string) map[string][]byte {
	read := cached(pictureStore(app), atMost(traced(app, avatarReader(app)), maxAvatarBytes))
	arrived := budget.fetch(ctx, read, wanted)
	app.Tracer.Log(observability.TraceTUI, "avatars read",
		"wanted", len(wanted), "arrived", len(arrived))
	return arrived
}

// traced says what happened to each avatar. A picture that does not appear is
// three failures wearing one face — never asked for, never arrived, never drawn —
// and only the log can tell them apart on somebody else's terminal.
func traced(app *appctx.App, read imageReader) imageReader {
	return func(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
		data, err := read(ctx, source, maxBytes)
		app.Tracer.Log(observability.TraceTUI, "picture fetched",
			"source", source, "bytes", len(data), "err", fmt.Sprint(err))
		return data, err
	}
}

// atMost caps what a reader may spend on one picture, under whatever the budget
// has left. An avatar is four cells of a face and has no business costing what a
// screenshot may.
func atMost(read imageReader, ceiling int64) imageReader {
	return func(ctx context.Context, source string, maxBytes int64) ([]byte, error) {
		return read(ctx, source, min(maxBytes, ceiling))
	}
}

// The two sizes a person's picture is drawn at.
//
// A cell is about twice as tall as it is wide, so four columns and two rows is
// the square the web draws beside a name — two columns would come out one row
// high. Which is exactly what a reaction wants: two columns and the one row a
// pill has room for is the same square, small.
const (
	avatarCols = 4
	avatarRows = 2

	chipCols = 2
)

// facesPlacedMsg carries pictures whose pixels the terminal already holds, so
// the cells pointing at them can go on screen.
type facesPlacedMsg struct {
	drawn map[faceAt]tui.RenderedImage
}

func placeFaces(drawn map[faceAt]tui.RenderedImage) tea.Cmd {
	return func() tea.Msg { return facesPlacedMsg{drawn: drawn} }
}

// besideFace puts a person's picture to the left of what they wrote or are
// called, padding whichever side is shorter so the block stays rectangular.
func besideFace(cells, lines []string) []string {
	if len(cells) == 0 {
		return lines
	}

	gap := "  "
	blank := strings.Repeat(" ", avatarCols) + gap
	rows := make([]string, 0, max(len(cells), len(lines)))
	for index := range max(len(cells), len(lines)) {
		left := blank
		if index < len(cells) {
			left = cells[index] + gap
		}
		right := ""
		if index < len(lines) {
			right = lines[index]
		}
		rows = append(rows, left+right)
	}
	return rows
}
